package schemaprofile

import "github.com/openbindings/openbindings-go/jsonvalue"

// EqualNormalizedSchemas compares normalized structure. Unordered matching
// applies only at schema union positions, never inside const/enum JSON data.
func EqualNormalizedSchemas(a, b map[string]any) (bool, error) {
	if len(a) != len(b) {
		return false, nil
	}
	for _, k := range sortedMapKeys(a) {
		av := a[k]
		bv, present := b[k]
		if !present {
			return false, nil
		}
		if k == "oneOf" || k == "anyOf" {
			aa, ao := asSlice(av)
			ba, bo := asSlice(bv)
			if ao && bo {
				if len(aa) != len(ba) {
					return false, nil
				}
				used := make([]bool, len(ba))
				for _, v := range aa {
					found := false
					for i, w := range ba {
						if used[i] {
							continue
						}
						vm, vo := asMap(v)
						wm, wo := asMap(w)
						if !vo || !wo {
							continue
						}
						same, err := EqualNormalizedSchemas(vm, wm)
						if err != nil {
							return false, err
						}
						if same {
							used[i] = true
							found = true
							break
						}
					}
					if !found {
						return false, nil
					}
				}
				continue
			}
		}
		am, ao := asMap(av)
		bm, bo := asMap(bv)
		if k == "properties" && ao && bo {
			if len(am) != len(bm) {
				return false, nil
			}
			for _, name := range sortedMapKeys(am) {
				x, xo := asMap(am[name])
				y, yo := asMap(bm[name])
				if !xo || !yo {
					return false, nil
				}
				same, err := EqualNormalizedSchemas(x, y)
				if err != nil || !same {
					return same, err
				}
			}
			continue
		}
		if (k == "items" || k == "additionalProperties") && ao && bo {
			same, err := EqualNormalizedSchemas(am, bm)
			if err != nil || !same {
				return same, err
			}
			continue
		}
		same, err := jsonvalue.Equal(av, bv)
		if err != nil || !same {
			return same, err
		}
	}
	return true, nil
}
