module github.com/kafeiih/vogel/examples/bpms

go 1.26.7

toolchain go1.26.10

require (
	cel.dev/cel-go v0.32.0
	github.com/kafeiih/vogel v0.13.2
	github.com/stretchr/testify v1.12.1
)

require (
	cel.dev/expr v0.25.3 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/go-chi/chi/v5 v5.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20261011100332-faecfb24eb49 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261005182115-fad411399dd8 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261005182115-fad411399dd8 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/kafeiih/vogel => ../..
