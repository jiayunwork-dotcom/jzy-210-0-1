// Command balancer runs the field-balancing backend HTTP service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"balancer/internal/api"
	"balancer/internal/service"
	"balancer/internal/store"
)

func main() {
	addr := env("HTTP_ADDR", ":8080")
	backend := env("BALANCER_STORE", "mysql")

	var st store.Store
	switch backend {
	case "memory":
		st = store.NewMemory()
		log.Print("using in-memory store")
	case "mysql":
		dsn := env("MYSQL_DSN", "balancer:balancer@tcp(mysql:3306)/balancer?charset=utf8mb4&collation=utf8mb4_unicode_ci")
		dir := env("MIGRATIONS_DIR", "migrations")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var err error
		st, err = store.OpenMySQL(ctx, dsn, dir)
		cancel()
		if err != nil {
			log.Fatalf("open mysql: %v", err)
		}
		log.Print("connected to MySQL and migrated")
	default:
		log.Fatalf("unknown BALANCER_STORE %q", backend)
	}
	defer st.Close()

	svc := service.New(st)
	e := api.NewServer(svc)

	go func() {
		log.Printf("listening on %s", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
