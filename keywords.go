package main

import (
	_ "embed"
	"encoding/xml"
	"log"
	"strings"
)

// CLDR's English emoji annotations (search keywords), downloaded as is. The derived
// file covers sequences such as flags, keycaps and gendered variants. They come from
// CLDR's main branch because releases lag behind the newest emoji.
//
//go:generate curl -sfL https://raw.githubusercontent.com/unicode-org/cldr/main/common/annotations/en.xml -o data/cldr-annotations-en.xml
//go:generate curl -sfL https://raw.githubusercontent.com/unicode-org/cldr/main/common/annotationsDerived/en.xml -o data/cldr-annotations-derived-en.xml
var (
	//go:embed data/cldr-annotations-en.xml
	cldrAnnotationsXML []byte
	//go:embed data/cldr-annotations-derived-en.xml
	cldrAnnotationsDerivedXML []byte
)

// loadKeywords returns CLDR keywords by emoji, keyed without variation selectors
// (CLDR writes "❤" where emoji-test.txt has "❤️"); see keywordKey.
func loadKeywords() map[string][]string {
	out := map[string][]string{}
	for _, data := range [][]byte{cldrAnnotationsXML, cldrAnnotationsDerivedXML} {
		var doc struct {
			Annotations []struct {
				CP   string `xml:"cp,attr"`
				Type string `xml:"type,attr"`
				Text string `xml:",chardata"`
			} `xml:"annotations>annotation"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			log.Fatalf("parsing CLDR annotations: %v", err)
		}
		for _, a := range doc.Annotations {
			if a.Type == "tts" { // the emoji's name, which we already have
				continue
			}
			key := keywordKey(a.CP)
			for _, kw := range strings.Split(a.Text, "|") {
				out[key] = append(out[key], strings.TrimSpace(kw))
			}
		}
	}
	return out
}

func keywordKey(emoji string) string {
	return strings.ReplaceAll(emoji, "️", "")
}
