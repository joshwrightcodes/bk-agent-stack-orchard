package orchard

// The wire types of this package are generated from the Orchard resource types at the version
// that the target design pins. A pipeline runs the generator and fails when the result differs
// from the file in the tree, which is how the project learns that the contract moved.
//
// The -ref is the commit of the version that the design states. A tag can move, so the commit is
// the pin, and the generator stops when the tag no longer points at it.
//
//go:generate go run ../../tools/orchardgen -out types_gen.go -design ../../docs/architecture/target-design.md -ref 7e31732df67ed4c78ac1de075fa22e7ff3ce5608
