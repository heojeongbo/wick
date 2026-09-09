// SSH and SFTP are not a large dependency as these go, but they are still one
// somebody who carries to a directory has no use for. Each sink that needs
// something outside the standard library is a module of its own.
module github.com/heojeongbo/wick/sink/sftp

go 1.26.4

require (
	github.com/heojeongbo/wick v0.2.0
	github.com/lesomnus/z v0.0.0-20260907061127-c3858eb05878
	github.com/pkg/sftp v1.13.11
	golang.org/x/crypto v0.57.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)
