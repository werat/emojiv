package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"strings"
)

// emoji-test.txt is Unicode's emoji list, downloaded as is. Refresh it with `go generate`.
//
//go:generate curl -sfL https://unicode.org/Public/emoji/latest/emoji-test.txt -o data/emoji-test.txt
//go:embed data/emoji-test.txt
var emojiTestTxt string

// maxChoiceOptions is the Jev limit on options per Choice question.
const maxChoiceOptions = 255

// bucketPreviewNames is how many emoji names describe a bucket in the category question.
const bucketPreviewNames = 12

type Emoji struct {
	Char     string `json:"emoji"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Subgroup string `json:"subgroup"`
}

// Bucket is a set of emoji asked about in a single Choice question.
// Buckets follow Unicode subgroups, split when a subgroup exceeds maxChoiceOptions.
type Bucket struct {
	ID          string
	Group       string
	Description string
	Emojis      []Emoji
}

// loadEmojis parses Unicode's emoji-test.txt, keeping fully-qualified emoji
// without skin tone variants.
func loadEmojis() []Emoji {
	var out []Emoji
	var group, subgroup string
	sc := bufio.NewScanner(strings.NewReader(emojiTestTxt))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# group: "):
			group = strings.TrimPrefix(line, "# group: ")
			continue
		case strings.HasPrefix(line, "# subgroup: "):
			subgroup = strings.TrimPrefix(line, "# subgroup: ")
			continue
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		}
		// Format: code points ; status # emoji E<version> name
		semi := strings.Index(line, ";")
		hash := strings.Index(line, "#")
		if semi < 0 || hash < semi {
			continue
		}
		if strings.TrimSpace(line[semi+1:hash]) != "fully-qualified" {
			continue
		}
		fields := strings.SplitN(strings.TrimSpace(line[hash+1:]), " ", 3)
		if len(fields) < 3 {
			continue
		}
		name := fields[2]
		if strings.Contains(name, "skin tone") {
			continue
		}
		out = append(out, Emoji{Char: fields[0], Name: name, Group: group, Subgroup: subgroup})
	}
	return out
}

func buildBuckets(emojis []Emoji) []Bucket {
	var order []string
	bySub := map[string][]Emoji{}
	for _, e := range emojis {
		if _, ok := bySub[e.Subgroup]; !ok {
			order = append(order, e.Subgroup)
		}
		bySub[e.Subgroup] = append(bySub[e.Subgroup], e)
	}

	var buckets []Bucket
	for _, sub := range order {
		list := bySub[sub]
		parts := (len(list) + maxChoiceOptions - 1) / maxChoiceOptions
		size := (len(list) + parts - 1) / parts
		for i := range parts {
			chunk := list[i*size : min((i+1)*size, len(list))]
			id := sub
			if parts > 1 {
				id = fmt.Sprintf("%s-%d", sub, i+1)
			}
			buckets = append(buckets, Bucket{
				ID:          id,
				Group:       chunk[0].Group,
				Description: describeBucket(sub, chunk),
				Emojis:      chunk,
			})
		}
	}
	return buckets
}

func describeBucket(subgroup string, list []Emoji) string {
	var names []string
	for i, e := range list {
		if i == bucketPreviewNames {
			names = append(names, "...")
			break
		}
		names = append(names, e.Name)
	}
	return fmt.Sprintf("%s (%s): %s", strings.ReplaceAll(subgroup, "-", " "), list[0].Group, strings.Join(names, ", "))
}
