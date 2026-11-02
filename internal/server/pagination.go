package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var shardSuffix = regexp.MustCompile(`^(.*)_([0-9]{1,2}|[0-9a-fA-F]{2})$`)

func fingerprint(v interface{}) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func paginateObjects(objects []objectInfo, args map[string]interface{}, instance, typ, pattern string) ([]objectInfo, bool, string, error) {
	bad := func() ([]objectInfo, bool, string, error) {
		return nil, false, "", fmt.Errorf("invalid pagination options or stale cursor/group")
	}
	size := 1000
	if v, ok := args["page_size"]; ok {
		n, ok := v.(float64)
		if !ok || n < 1 || n > 1000 || n != float64(int(n)) {
			return bad()
		}
		size = int(n)
	}
	grouping := true
	if v, ok := args["group_shards"]; ok {
		var valid bool
		grouping, valid = v.(bool)
		if !valid {
			return bad()
		}
	}
	gid := ""
	if v, ok := args["group_id"]; ok {
		var valid bool
		gid, valid = v.(string)
		if !valid {
			return bad()
		}
	}
	cur := ""
	if v, ok := args["cursor"]; ok {
		var valid bool
		cur, valid = v.(string)
		if !valid {
			return bad()
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		a, b := objects[i], objects[j]
		return a.Schema+"\x00"+a.Name+"\x00"+a.Type < b.Schema+"\x00"+b.Name+"\x00"+b.Type
	})
	buckets := map[string][]objectInfo{}
	keys := map[string]string{}
	for _, o := range objects {
		if o.Type != "table" {
			continue
		}
		m := shardSuffix.FindStringSubmatch(o.Name)
		if m == nil {
			continue
		}
		key := fingerprint([]string{o.Schema, m[1], fmt.Sprint(len(m[2]))})
		buckets[key] = append(buckets[key], o)
		keys[o.Schema+"\x00"+o.Name] = key
	}
	groups := map[string]objectInfo{}
	for key, members := range buckets {
		if len(members) < 4 {
			continue
		}
		m := shardSuffix.FindStringSubmatch(members[0].Name)
		rule := "decimal"
		ex := []string{}
		for _, o := range members {
			suffix := shardSuffix.FindStringSubmatch(o.Name)[2]
			if strings.ContainsAny(suffix, "abcdefABCDEF") {
				rule = "hex"
			}
			if len(ex) < 3 {
				ex = append(ex, o.Name)
			}
		}
		id := fingerprint([]string{instance, key})
		groups[key] = objectInfo{Kind: "table_group", Schema: members[0].Schema, Prefix: m[1], Rule: rule, MemberCount: len(members), Examples: ex, GroupID: id}
	}
	out := []objectInfo{}
	seen := map[string]bool{}
	found := false
	for _, o := range objects {
		key := keys[o.Schema+"\x00"+o.Name]
		g, ok := groups[key]
		if o.Type != "table" {
			ok = false
		}
		if gid != "" {
			if ok && g.GroupID == gid {
				out = append(out, o)
				found = true
			}
			continue
		}
		if grouping && ok {
			if !seen[key] {
				out = append(out, g)
				seen[key] = true
			}
		} else {
			out = append(out, o)
		}
	}
	if gid != "" && !found {
		return bad()
	}
	binding := fingerprint([]interface{}{instance, typ, pattern, grouping, gid, out})
	offset := 0
	if cur != "" {
		var c struct {
			Binding string
			Offset  int
		}
		b, e := base64.RawURLEncoding.DecodeString(cur)
		if e != nil {
			return bad()
		}
		if json.Unmarshal(b, &c) != nil || c.Binding != binding || c.Offset < 0 || c.Offset >= len(out) {
			return bad()
		}
		offset = c.Offset
	}
	end := offset + size
	if end > len(out) {
		end = len(out)
	}
	next := ""
	more := end < len(out)
	if more {
		b, _ := json.Marshal(struct {
			Binding string
			Offset  int
		}{binding, end})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out[offset:end], more, next, nil
}
