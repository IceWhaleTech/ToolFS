package toolfs

// RenameRequirement is what a rename needs beyond write (create) at the new
// name. Every ZimaOS path that renames — Files, SMB and this mount — applies the
// same rule:
//
//   - renaming within one folder without replacing anything needs write only:
//     the entry stays where it was, nothing is lost;
//   - moving to another folder also needs delete at the source: the entry
//     disappears from that folder, which is what a delete does there;
//   - replacing an existing entry also needs delete on that entry: its content
//     is gone afterwards.
type RenameRequirement struct {
	DeleteSource bool
	DeleteTarget bool
}

func renameRequirement(sameDir, overwrite bool) RenameRequirement {
	return RenameRequirement{DeleteSource: !sameDir, DeleteTarget: overwrite}
}
