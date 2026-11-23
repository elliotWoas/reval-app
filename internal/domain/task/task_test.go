package task

import "testing"

func TestParse(t *testing.T) {
	in := "۱. طراحی صفحه فرود\n\n!جلسه با مشتری\n- بستن گزارش ماهانه\n3) تست\n10:30 جلسه تیم"
	ts, err := Parse(in, 20, 200)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"طراحی صفحه فرود", "جلسه با مشتری", "بستن گزارش ماهانه", "تست", "10:30 جلسه تیم"}
	if len(ts) != len(want) {
		t.Fatalf("got %d tasks: %+v", len(ts), ts)
	}
	for i, w := range want {
		if ts[i].Text != w {
			t.Errorf("task %d = %q, want %q", i, ts[i].Text, w)
		}
	}
	if !ts[1].Important || ts[0].Important {
		t.Error("importance flags wrong")
	}
	if ts[0].Progress != -1 || ts[0].Source != SourceNew {
		t.Error("defaults wrong")
	}
	if _, err := Parse("  \n ", 20, 200); err == nil {
		t.Error("empty should fail")
	}
	if _, err := Parse("a\nb\nc", 2, 200); err == nil {
		t.Error("too many should fail")
	}
}
