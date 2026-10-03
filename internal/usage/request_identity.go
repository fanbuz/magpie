package usage

// identityIndex supports exact identity matching without enumerating the
// Cartesian product of repeated IDs. Projections let missing session/other-ID
// fields act as unknowns while explicit conflicts remain incompatible.
type identityKey struct {
	computer, agent, session, id, other string
	kind, any                           uint8
}
type identityBucket struct {
	count int
	only  rowRef
}
type identityIndex map[identityKey]identityBucket

func refComputer(ref rowRef) string {
	if ref.Chunk.Computer != "" {
		return ref.Chunk.Computer
	}
	return ref.Chunk.Strings[ref.Chunk.Rows[ref.Index].Text[rowComputer]]
}

func identityMask(p packedRow) uint8 {
	var mask uint8
	if p.Text[rowRequestID] != 0 {
		mask |= 1
	}
	if p.Text[rowResponseID] != 0 {
		mask |= 2
	}
	return mask
}

func identityKeys(ref rowRef) [2]identityKey {
	c, p := ref.Chunk, ref.Chunk.Rows[ref.Index]
	text := func(i int) string { return c.Strings[p.Text[i]] }
	agent := text(0)
	// These clients share Claude message/request identities. Session and
	// computer still have to agree whenever both observations know them.
	switch agent {
	case "claude", "claude-desktop", "cowork":
		agent = "claude"
	}
	session := text(14)
	if session == "" {
		session = text(13)
	}
	k := identityKey{computer: refComputer(ref), agent: agent, session: session, id: text(rowRequestID), other: text(rowResponseID)}
	r := k
	r.kind, r.id, r.other = 1, k.other, k.id
	return [2]identityKey{k, r}
}

func (idx identityIndex) add(ref rowRef) {
	for _, key := range identityKeys(ref) {
		if key.id == "" {
			continue
		}
		for projection := uint8(0); projection < 4; projection++ {
			k := key
			k.any = projection
			if projection&1 != 0 {
				k.session = ""
			}
			if projection&2 != 0 {
				k.other = ""
			}
			b := idx[k]
			b.count++
			b.only = ref
			idx[k] = b
		}
	}
}

func (idx identityIndex) find(ref rowRef) (rowRef, int) {
	var only rowRef
	count := 0
	for _, key := range identityKeys(ref) {
		if key.id == "" {
			continue
		}
		// Disjoint exact/empty buckets when known; a projection across all
		// values when unknown. Never count the same row twice within an ID.
		sessions, others := [2]string{key.session, ""}, [2]string{key.other, ""}
		ns, no := 2, 2
		if key.session == "" {
			key.any |= 1
			ns = 1
		}
		if key.other == "" {
			key.any |= 2
			no = 1
		}
		for _, session := range sessions[:ns] {
			for _, other := range others[:no] {
				k := key
				k.session, k.other = session, other
				b := idx[k]
				if b.count == 0 {
					continue
				}
				if b.count > 1 || count == 1 && only != b.only {
					return rowRef{}, 2
				}
				only, count = b.only, 1
			}
		}
	}
	return only, count
}
