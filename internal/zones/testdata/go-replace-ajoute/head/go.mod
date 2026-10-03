module example.com/app

go 1.27

require (
	github.com/foo/bar v1.2.0
	github.com/baz/qux v0.3.0 // indirect
)

replace github.com/foo/bar => github.com/evil/bar v1.2.0
