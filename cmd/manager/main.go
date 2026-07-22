// Command manager runs Hardcore Together Manager: process lifecycle,
// archive/records, and the MOD⇔Manager / Gate⇔Manager TCP+NDJSON servers
// (architecture-manager.md).
//
// This is the composition root for the layered architecture
// (domain/port/application/adapter, architecture-manager.md): it is the
// only place concrete adapters are constructed and wired into
// application.ChallengeApplicationService via internal/port's interfaces.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/config"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/fsarchive"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/fsrecords"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/fsstate"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/gateserver"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/modserver"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/osprocess"
	"github.com/RayLight1732/hardcore-together-manager/internal/adapter/systemclock"
	"github.com/RayLight1732/hardcore-together-manager/internal/application"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "path to config.yml (architecture-manager.md 9節)")
	flag.Parse()

	if err := run(*configPath); err != nil {
		log.Fatalf("manager: %v", err)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	// --- adapters ---
	state, err := fsstate.New(cfg.State.Path)
	if err != nil {
		return err
	}
	proc := osprocess.New(cfg.Hardcore.WorkDir, cfg.Hardcore.StartCommand, cfg.Hardcore.PidFile)
	archiveRepo := fsarchive.New(cfg.Archive.Dir, cfg.Hardcore.WorldDir())
	recordsRepo := fsrecords.New(cfg.Hardcore.RecordsPath())
	clock := systemclock.Clock{}

	stopTimeout := time.Duration(cfg.Timeouts.ProcessStopSeconds) * time.Second

	// A previous Manager instance that crashed (rather than shutting down
	// gracefully via SIGTERM) may have left a hardcore process orphaned.
	// This must complete before the Gate⇔Manager server starts accepting
	// commands, so a stale orphan is never mistaken for "not running" and
	// double-started onto the same world/ and port (architecture-manager.md
	// 3節).
	if err := proc.ReapOrphan(stopTimeout); err != nil {
		return fmt.Errorf("reap orphaned hardcore process: %w", err)
	}

	modAddr := fmt.Sprintf("127.0.0.1:%d", cfg.SignalPort)
	modSrv := modserver.NewServer(modAddr)
	gateSrv := gateserver.NewServer(cfg.GateListenAddr)

	// --- application ---
	// modSrv/gateSrv are mutually referential with the service (see both
	// adapters' NewServer doc comments): they're built above without an
	// Application, the service is built here against their port
	// interfaces (modSrv as ReadyWaiter, gateSrv as GateNotifier), then
	// wired back into the adapters as their Application dependency.
	svc := application.New(application.Deps{
		State:   state,
		Process: proc,
		World:   proc,
		Archive: archiveRepo,
		Records: recordsRepo,
		Gate:    gateSrv,
		Ready:   modSrv,
		Clock:   clock,
	}, application.Timeouts{
		Evacuate:    time.Duration(cfg.Timeouts.EvacuateCompleteSeconds) * time.Second,
		Ready:       time.Duration(cfg.Timeouts.HardcoreReadySeconds) * time.Second,
		ProcessStop: time.Duration(cfg.Timeouts.ProcessStopSeconds) * time.Second,
	})
	modSrv.SetApplication(svc)
	gateSrv.SetApplication(svc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := modSrv.ListenAndServe(ctx); err != nil {
			errCh <- fmt.Errorf("modserver: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := gateSrv.ListenAndServe(ctx); err != nil {
			errCh <- fmt.Errorf("gateserver: %w", err)
		}
	}()

	log.Printf("manager: listening (mod=%s, gate=%s)", modAddr, cfg.GateListenAddr)

	var runErr error
	select {
	case <-ctx.Done():
		log.Printf("manager: shutdown signal received")
	case runErr = <-errCh:
		log.Printf("manager: server error: %v", runErr)
		stop()
	}

	// Manager is the parent of the hardcore process (os/exec, spec 1節); on
	// a clean shutdown, stop it too rather than leave it orphaned with no
	// supervisor.
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout+5*time.Second)
	defer cancel()
	if err := proc.Stop(stopCtx, stopTimeout); err != nil {
		log.Printf("manager: error stopping hardcore process during shutdown: %v", err)
	}

	wg.Wait()
	return runErr
}
