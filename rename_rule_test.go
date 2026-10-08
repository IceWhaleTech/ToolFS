package toolfs

import "testing"

// 改名的权限口径：同目录且不覆盖只要写；跨目录要求源可删；覆盖要求目标可删。
func TestRenameRequirement(t *testing.T) {
	cases := []struct {
		name               string
		sameDir, overwrite bool
		want               RenameRequirement
	}{
		{"rename in place", true, false, RenameRequirement{}},
		{"move to another folder", false, false, RenameRequirement{DeleteSource: true}},
		{"rename over an existing entry", true, true, RenameRequirement{DeleteTarget: true}},
		{"move over an existing entry", false, true, RenameRequirement{DeleteSource: true, DeleteTarget: true}},
	}
	for _, c := range cases {
		if got := renameRequirement(c.sameDir, c.overwrite); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
