package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

// emoji-test.txt is Unicode's emoji list, downloaded as is. Refresh it with `go generate`.
//
//go:generate curl -sfL https://unicode.org/Public/emoji/latest/emoji-test.txt -o data/emoji-test.txt
//go:embed data/emoji-test.txt
var emojiTestTxt string

// maxChoiceOptions is the Jev limit on options per Choice question.
const maxChoiceOptions = 255

type Emoji struct {
	Char     string   `json:"emoji"`
	Name     string   `json:"name"`
	Group    string   `json:"group"`
	Subgroup string   `json:"subgroup"`
	Keywords []string `json:"keywords,omitempty"` // from CLDR, without the name itself
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
// without skin tone variants, and attaches CLDR keywords.
func loadEmojis() []Emoji {
	keywords := loadKeywords()
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
		e := Emoji{Char: fields[0], Name: name, Group: group, Subgroup: subgroup}
		for _, kw := range keywords[keywordKey(e.Char)] {
			if kw != name {
				e.Keywords = append(e.Keywords, kw)
			}
		}
		out = append(out, e)
	}
	return out
}

// variantKey groups emoji that are variants of one concept, such as the gendered
// forms of a profession, the family compositions, or the 24 clock faces. Search
// results show only the best match of each group.
func variantKey(e Emoji) string {
	switch {
	case strings.HasPrefix(e.Name, "flag: "):
		return e.Name // baseName would reduce it to the bare country name
	case e.Subgroup == "time" && (strings.HasSuffix(e.Name, " o’clock") || strings.HasSuffix(e.Name, "-thirty")):
		return "clock face"
	}
	return baseName(e.Name)
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
	seen := map[string]bool{}
	for _, e := range list {
		name := baseName(e.Name)
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return fmt.Sprintf("%s (%s): %s", strings.ReplaceAll(subgroup, "-", " "), list[0].Group, strings.Join(names, ", "))
}

var (
	personWords = map[string]string{
		"man": "person", "woman": "person", "person": "person",
		"men": "people", "women": "people", "people": "people",
	}
	familyMembers = map[string]bool{
		"man": true, "woman": true, "person": true, "adult": true,
		"boy": true, "girl": true, "child": true,
	}
	// A dropped leading person word would leave a dangling "in tuxedo" or "with veil".
	keepPersonBefore = map[string]bool{"with": true, "in": true, "wearing": true}
	// "woman and man holding hands" means the same as "people holding hands".
	mixedCouple = regexp.MustCompile(`\b(man|woman) and (man|woman)\b`)
)

// baseName strips gender and direction variants from an emoji name so that e.g.
// "student", "man student" and "woman student" all describe the bucket as "student".
func baseName(name string) string {
	name = strings.TrimSuffix(name, " facing right")
	if head, tail, ok := strings.Cut(name, ": "); ok {
		if head == "flag" {
			return tail // the bucket is already labeled "country flag"
		}
		if _, isPerson := personWords[head]; isPerson {
			return tail // "man: beard" -> "beard"
		}
		all := true
		for _, m := range strings.Split(tail, ", ") {
			all = all && familyMembers[m]
		}
		if all {
			return head // "family: man, woman, boy" -> "family"
		}
		return name
	}
	name = mixedCouple.ReplaceAllString(name, "people")

	words := strings.Fields(name)
	var out []string
	for i, w := range words {
		neutral, isPerson := personWords[w]
		if !isPerson {
			out = append(out, w)
			continue
		}
		if i == len(words)-1 || keepPersonBefore[words[i+1]] {
			out = append(out, neutral) // "deaf man" -> "deaf person", "man in tuxedo" -> "person in tuxedo"
		}
		// Otherwise drop it: "man student" -> "student".
	}
	return strings.Join(out, " ")
}
