package main

import (
	"context"
	"fmt"
	"os"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

func main() {
	store, err := vault.Open(context.Background(), os.Args[1], []byte(os.Args[2]))
	if err != nil { panic(err) }
	entries, _ := store.Entries(context.Background(), "venice-web", "https://outerface.venice.ai/api")
	for _, e := range entries {
		cred, _ := webprov.ParseCredential(e.Key)
		fmt.Println(e.Label, "clerk:", len(cred.Clerk), "venice:", len(cred.Venice), "ua:", cred.UserAgent[:20])
		for _, c := range cred.Clerk { fmt.Println("  clerk cookie", c.Name, len(c.Value)) }
		if e.Label == "pro" {
			pr := webprov.New(30000000000).Principal(context.Background(), cred)
			fmt.Println("  principal:", pr)
		}
	}
}
