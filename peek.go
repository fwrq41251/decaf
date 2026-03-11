package main

import (
	"fmt"
	"os"

	sdb "github.com/fwrq41251/decaf/internal/semanticdb"
	"google.golang.org/protobuf/proto"
)

func main() {
	path := "../jlox/target/classes/META-INF/semanticdb/src/main/java/org/winry/Lox.java.semanticdb"
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		return
	}
	var docs sdb.TextDocuments
	if err := proto.Unmarshal(data, &docs); err != nil {
		fmt.Printf("Error unmarshaling: %v\n", err)
		return
	}
	for _, doc := range docs.Documents {
		fmt.Printf("Document URI: %s\n", doc.Uri)
		for _, occ := range doc.Occurrences {
			if occ.Range != nil && occ.Range.StartLine <= 25 && occ.Range.EndLine >= 15 {
				fmt.Printf("  Occ: %s at %d:%d-%d:%d (Role: %v)\n", 
					occ.Symbol, occ.Range.StartLine, occ.Range.StartCharacter, 
					occ.Range.EndLine, occ.Range.EndCharacter, occ.Role)
			}
		}
	}
}
