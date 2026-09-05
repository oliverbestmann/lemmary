{
  description = "Lemmary dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        # bleve reaches FAISS through github.com/blevesearch/go-faiss, which is
        # written against blevesearch's fork of FAISS: it adds the `*_c_ex.h` C
        # entry points go-faiss calls, which upstream (and so nixpkgs' pkgs.faiss)
        # does not have. Same pin as scripts/faiss-build.sh's FAISS_REF -- bump
        # both together, from bleve's docs/vectors.md compatibility table.
        faiss = pkgs.faiss.overrideAttrs (old: {
          src = pkgs.fetchFromGitHub {
            owner = "blevesearch";
            repo = "faiss";
            rev = "8a59a0c552fa2d14fa871f6b6bc793de1d277f5e";
            hash = "sha256-32GY0wQUNQ9FMUFbFykDdeakK1qNcuqe6OaLTRWGClg=";
          };

          # Shared libs (go-faiss dlopens/cgo-links libfaiss_c.so, it does not
          # want a static archive) and no Python bindings (this fork is built
          # for the Go C API only, so swig/numpy are dead weight here).
          cmakeFlags = [
            "-DBUILD_SHARED_LIBS=ON"
            "-DFAISS_ENABLE_C_API=ON"
            "-DFAISS_ENABLE_GPU=OFF"
            "-DFAISS_ENABLE_PYTHON=OFF"
            "-DFAISS_OPT_LEVEL=generic"
          ];
          buildFlags = [ "faiss" "faiss_c" ];
          nativeBuildInputs = pkgs.lib.filter
            (p: !(pkgs.lib.hasPrefix "swig" (p.pname or "")))
            old.nativeBuildInputs;
          buildInputs = pkgs.lib.filter
            (p: !(pkgs.lib.hasInfix "numpy" (p.pname or "")))
            old.buildInputs;
          propagatedBuildInputs = [ ];

          # Upstream's postBuild/postInstall package the Python wheel this fork
          # is not built with; there is no `dist` output to fill without them.
          postBuild = "";
          postInstall = "";
          outputs = [ "out" ];

          # FAISS's own CMakeLists passes -Wno-format, which drops nix
          # hardening's paired -Wformat and leaves its -Werror=format-security
          # with nothing to silence -- every TU then fails on a warning about
          # a flag being ignored. Turning off the "format" hardening flag is
          # the fix nixpkgs itself uses for packages with the same clash.
          hardeningDisable = [ "format" ];
        });
      in
      {
        packages.faiss = faiss;

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.nodejs_24
            pkgs.pnpm
            pkgs.poppler-utils
            faiss
          ];

          # Same variables .envrc sets for a manually-built $HOME/.local/faiss,
          # pointed at the Nix store copy instead: bleve's vectors build tag,
          # cgo on, and the compiler/loader told where FAISS's headers and
          # shared libraries live.
          GOFLAGS = "-tags=vectors";
          CGO_ENABLED = "1";
          CGO_CFLAGS = "-I${faiss}/include";
          CGO_LDFLAGS = "-L${faiss}/lib";
          LD_LIBRARY_PATH = "${faiss}/lib";
        };
      });
}
