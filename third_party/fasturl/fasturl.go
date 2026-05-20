package fasturl

type URL struct {
	raw string
}

func Parse(raw string) (*URL, error) {
	return &URL{raw: raw}, nil
}

func (u *URL) String() string {
	if u == nil {
		return ""
	}
	return u.raw
}
