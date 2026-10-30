// Command drop_workspace_db permanently deletes a CANCELED workspace's dedicated
// database. It is an operator tool, never called by the API.
//
//	DATABASE_URL=postgres://... go run ./cmd/drop_workspace_db \
//	    -tenant <tenant-uuid> -confirm recurso_<id>
//
// It refuses unless the workspace is canceled and -confirm is the exact database
// name. Back the database up first (see docs/dedicated-databases).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/recurso-dev/recurso/internal/adapter/db"
)

func main() {
	tenant := flag.String("tenant", "", "workspace (tenant) id")
	confirm := flag.String("confirm", "", "the exact database name, e.g. recurso_acme")
	flag.Parse()

	id, err := uuid.Parse(*tenant)
	if err != nil {
		log.Fatalf("-tenant must be a UUID: %v", err)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	shared, err := db.NewConnection(dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name, err := db.DropWorkspaceDatabase(ctx, dsn, shared, id, *confirm)
	if err != nil {
		log.Fatalf("refused: %v", err)
	}
	fmt.Printf("dropped database %s for tenant %s\n", name, id)
}
