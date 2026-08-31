package toolfs

import (
	"reflect"
	"testing"
)

func TestViewPathAppliesZimaAliases(t *testing.T) {
	for _, c := range []struct {
		host string
		want string
	}{
		// Home: all three spellings collapse to /Home, and the longer
		// "/DATA/.home" prefix beats "/DATA".
		{"/DATA/.home/casaos", "/Home/casaos"},
		{"/DATA/.home/casaos/topics/Work/AAAA", "/Home/casaos/topics/Work/AAAA"},
		{"/media/.home/subuser", "/Home/subuser"},
		// The homes reached via the /media spelling of the system disk collapse
		// to /Home too, so .home never surfaces beneath /ZimaOS-HD.
		{"/media/ZimaOS-HD/.home/casaos", "/Home/casaos"},
		{"/media/ZimaOS-HD/.home", "/Home"},
		{"/home/casaos", "/Home/casaos"},
		// The system disk: /DATA and /media/ZimaOS-HD are the same space.
		{"/DATA", "/ZimaOS-HD"},
		{"/DATA/Documents/x", "/ZimaOS-HD/Documents/x"},
		{"/media/ZimaOS-HD/Documents/x", "/ZimaOS-HD/Documents/x"},
		// Other disks: /media stripped, label becomes the top-level space.
		{"/media/Backup/Movies", "/Backup/Movies"},
		{"/media/ZimaCube-HD/x", "/ZimaCube-HD/x"},
		// No alias → mirrored unchanged.
		{"/srv/something", "/srv/something"},
		// Trailing slash and duplicate separators are folded.
		{"/DATA/.home/casaos/", "/Home/casaos"},
		{"/media//Backup///Movies", "/Backup/Movies"},
	} {
		if got := viewPath(c.host, DefaultZimaViewAliases); got != c.want {
			t.Errorf("viewPath(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// The same host filesystem reached by two spellings must land on one view path,
// or the double-mounted /DATA and /media/ZimaOS-HD would appear as two spaces.
func TestViewPathFoldsTheDoubleMountedSystemDisk(t *testing.T) {
	a := viewPath("/DATA/foo/bar", DefaultZimaViewAliases)
	b := viewPath("/media/ZimaOS-HD/foo/bar", DefaultZimaViewAliases)
	if a != b {
		t.Errorf("the two spellings of the system disk diverged: %q vs %q", a, b)
	}
	if a != "/ZimaOS-HD/foo/bar" {
		t.Errorf("system disk view = %q, want /ZimaOS-HD/foo/bar", a)
	}
}

func TestViewSegments(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"/Home/casaos/topics/Work", []string{"Home", "casaos", "topics", "Work"}},
		{"/ZimaOS-HD", []string{"ZimaOS-HD"}},
		{"/", nil},
		{"", nil},
	} {
		if got := viewSegments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("viewSegments(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
