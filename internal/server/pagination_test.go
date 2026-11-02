package server

import "testing"

func TestPaginationBindings(t *testing.T) {
	objects := []objectInfo{{Type: "table", Name: "a"}, {Type: "table", Name: "b"}}
	_, more, c, e := paginateObjects(objects, map[string]interface{}{"page_size": float64(1)}, "one", "table", "")
	if e != nil || !more || c == "" {
		t.Fatal(more, c, e)
	}
	for _, id := range []string{"two"} {
		if _, _, _, e = paginateObjects(objects, map[string]interface{}{"cursor": c}, id, "table", ""); e == nil {
			t.Fatal("cross instance cursor accepted")
		}
	}
	if _, _, _, e = paginateObjects(objects[:1], map[string]interface{}{"cursor": c}, "one", "table", ""); e == nil {
		t.Fatal("stale cursor accepted")
	}
	for _, v := range []interface{}{float64(0), float64(1001), float64(1.5), "1"} {
		if _, _, _, e = paginateObjects(objects, map[string]interface{}{"page_size": v}, "one", "table", ""); e == nil {
			t.Fatal("bad size accepted", v)
		}
	}
}
