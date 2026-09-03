package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/rezkyrafael2901/vrouter-go/api"
	"github.com/rezkyrafael2901/vrouter-go/config"
	"github.com/rezkyrafael2901/vrouter-go/provider"
	"github.com/rezkyrafael2901/vrouter-go/router"
	"github.com/rezkyrafael2901/vrouter-go/stats"
)

const (
	VERSION = "1.0.0"
	APP     = "vrouter-go"
)

func main() {
	// Load config
	configPath := os.Getenv("VROUTER_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize provider registry from config
	provider.InitFromConfig(cfg.Providers)

	// Restore learned state from disk
	if err := stats.Load(); err != nil {
		log.Printf("Warning: could not load state: %v", err)
	}

	// Initialize router
	rt := router.NewRouter(cfg)

	// Print banner
	printBanner(cfg, rt)

	// Start health check loop (background goroutine)
	go rt.StartHealthCheckLoop()

	// Start stats persistence loop (every 30s)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := stats.Persist(); err != nil {
				log.Printf("stats persist error: %v", err)
			}
		}
	}()

	// Create Fiber app
	app := fiber.New(fiber.Config{
		AppName:      fmt.Sprintf("%s v%s", APP, VERSION),
		BodyLimit:    100 * 1024 * 1024, // 100MB
		Concurrency:  256 * 1024,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
	})

	// Middleware
	app.Use(logger.New(logger.Config{
		Format:     "${time} | ${status} | ${latency} | ${method} | ${path} | ${bytesWritten} | ${ip}\n",
		TimeFormat: "2006-01-02 15:04:05",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Content-Type,Authorization,Accept",
	}))

	// Serve dashboard.html as root
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendFile("dashboard.html")
	})

	// Mount API routes
	a := api.NewAPI(cfg)
	a.Register(app)

	// Health check endpoint
	app.Get("/v1/health", func(c *fiber.Ctx) error {
		healthy := 0
		for _, p := range provider.All() {
			if p.IsHealthy() {
				healthy++
			}
		}
		return c.JSON(fiber.Map{
			"ok":      true,
			"version": VERSION,
			"providers": healthy,
		})
	})

	// Mount chat completions endpoint
	app.Post("/v1/chat/completions", rt.HandleRequest)
	app.Post("/chat/completions", rt.HandleRequest)

	// CORS preflight
	app.Options("/*", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "20129"
	}
	addr := fmt.Sprintf(":%s", port)
	log.Printf("Starting %s on %s...", APP, addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func printBanner(cfg *config.Config, rt *router.Router) {
	providerCount := len(provider.All())
	comboCount := len(cfg.Combos)

	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Printf("║  %-12s v%-8s                        ║\n", APP, VERSION)
	fmt.Println("║  Multi-provider LLM routing gateway (Go)           ║")
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Printf("║  Providers:     %-34d║\n", providerCount)
	fmt.Printf("║  Combos:        %-34d║\n", comboCount)
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Println("║  Endpoints:                                         ║")
	fmt.Println("║    POST /v1/chat/completions  (OpenAI-compatible)   ║")
	fmt.Println("║    GET  /api/status                                 ║")
	fmt.Println("║    GET  /api/model-stats                            ║")
	fmt.Println("║    GET  /api/health-score                           ║")
	fmt.Println("║    GET  /api/circuit-breakers                       ║")
	fmt.Println("║    GET  /api/throughput                             ║")
	fmt.Println("║    GET  /v1/health                                  ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
}
