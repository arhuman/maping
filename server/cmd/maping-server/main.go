// Command maping-server is the mAPI-ng collector: it serves the IngestService
// (Connect/gRPC over h2c) and the minimal dashboard on one HTTP listener. When
// MAPING_POSTGRES_DSN is set it authenticates ingest keys and resolves per-
// tenant guardrail limits against the real control plane; without it, it falls
// back to a static dev-key resolver and default guardrails so local dev needs
// no Postgres.
//
// All wiring lives in the app package (so it is unit-testable without Postgres);
// this file is a thin shell that builds the logger and delegates to app.Run. The
// community binary passes no options, so it composes only the core surfaces; a
// composed build would pass app.WithRoutes/app.WithBackgroundJob here.
package main

import (
	"log/slog"
	"os"

	"github.com/arhuman/maping/server/app"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := app.Run(log); err != nil {
		log.Error("server exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}
