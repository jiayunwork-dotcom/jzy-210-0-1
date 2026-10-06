// Command balancing starts the field-balancing backend service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"balancing/internal/api"
	"balancing/internal/store"
)

func main() {
	addr := getenv("HTTP_ADDR", ":8080")
	backend := getenv("BACKEND", "mysql")

	var s store.Store
	switch backend {
	case "memory":
		// Convenience for local unit-style runs without a database.
		s = store.NewMemoryStore()
		log.Print("using in-memory store (ephemeral)")
	case "mysql":
		dsn := getenv("MYSQL_DSN", "")
		if dsn == "" {
			dsn = buildDSN()
		}
		mustWaitForMySQL(dsn)
		ms, err := store.NewMySQLStore(dsn)
		if err != nil {
			log.Fatalf("open mysql: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := ms.Migrate(ctx); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		cancel()
		s = ms
	default:
		log.Fatalf("unknown BACKEND %q", backend)
	}

	srv := api.NewServer(s)
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}

	go func() {
		log.Printf("listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}
	if err := s.Close(); err != nil {
		log.Printf("close store: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func buildDSN() string {
	host := getenv("MYSQL_HOST", "db")
	port := getenv("MYSQL_PORT", "3306")
	user := getenv("MYSQL_USER", "balancer")
	pass := getenv("MYSQL_PASSWORD", "balancer")
	db := getenv("MYSQL_DATABASE", "balancing")
	timeoutSec, _ := strconv.Atoi(getenv("MYSQL_TIMEOUT_SECONDS", "5"))
	return user + ":" + pass + "@tcp(" + host + ":" + port + ")/" + db +
		"?parseTime=true&multiStatements=true&timeout=" + strconv.Itoa(timeoutSec) + "s&charset=utf8mb4"
}

// mustWaitForMySQL retries the initial connection until the database accepts
// it (docker compose starts both containers in parallel).
func mustWaitForMySQL(dsn string) {
	const maxWait = 90 * time.Second
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		ms, err := store.NewMySQLStore(dsn)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err = ms.Ping(ctx)
			cancel()
			_ = ms.Close()
			if err == nil {
				return
			}
		}
		log.Printf("waiting for MySQL: %v", err)
		time.Sleep(2 * time.Second)
	}
	log.Fatal("MySQL did not become ready in time")
}
