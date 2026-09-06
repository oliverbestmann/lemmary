// mailgate is its own module, not part of the server's.
//
// It shares nothing with the backend but a directory on disk: no database, no
// collections, no build tags, and none of the server's dependencies -- it does
// not link bleve, FAISS or PocketBase, and it needs no CGO. Keeping the module
// separate is what makes that true rather than merely intended, and keeps four
// mail and PDF dependencies out of the binary that holds the archive.
module lemmary/mailgate

go 1.27.1

require (
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-smtp v0.25.0
	github.com/go-pdf/fpdf v0.9.0
	github.com/pdfcpu/pdfcpu v0.15.0
	golang.org/x/text v0.41.0
)

require (
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6 // indirect
	github.com/hhrutter/tiff v1.0.6 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/image v0.44.0 // indirect
)
