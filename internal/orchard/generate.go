package orchard

// The wire types of this package are generated from the Orchard resource types at the version
// that the target design pins. A pipeline runs the generator and fails when the result differs
// from the file in the tree, which is how the project learns that the contract moved.
//
//go:generate go run ../../tools/orchardgen -out types_gen.go -design ../../docs/architecture/target-design.md
