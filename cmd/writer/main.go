package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	chi "github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/kelseyhightower/envconfig"
	"github.com/shipperizer/miniature-monkey/v2/config"
	"github.com/shipperizer/miniature-monkey/v2/core"
	monConfig "github.com/shipperizer/miniature-monkey/v2/monitoring/config"
	monCore "github.com/shipperizer/miniature-monkey/v2/monitoring/core"
	"go.uber.org/zap"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type EnvSpec struct {
	Port      string `envconfig:"http_port" default:"8000"`
	ConfigMap string `envconfig:"configmap" default:"test"`
}

type Blueprint struct {
	configMap string
	client    *kubernetes.Clientset
}

func (b *Blueprint) Routes(router *chi.Mux) {
	router.Get(
		"/api/v0/sonar",
		func(w http.ResponseWriter, r *http.Request) {

			cm, err := b.client.CoreV1().ConfigMaps(apiv1.NamespaceDefault).Get(r.Context(), b.configMap, metav1.GetOptions{})

			if err != nil {
				json.NewEncoder(w).Encode(map[string]string{"sonar": err.Error()})
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			sec := time.Now().Second()
			cm.Data["file.txt"] = fmt.Sprintf("%s\nsonar-%v", cm.Data["file.txt"], sec)

			if _, err := b.client.CoreV1().ConfigMaps(apiv1.NamespaceDefault).Update(r.Context(), cm, metav1.UpdateOptions{}); err != nil {
				json.NewEncoder(w).Encode(map[string]string{"sonar": err.Error()})
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			w.WriteHeader(http.StatusOK)
		},
	)
}

func NewBlueprint(configMap string, client *kubernetes.Clientset) *Blueprint {
	b := new(Blueprint)

	b.client = client
	b.configMap = configMap

	return b
}

func main() {
	logger, err := zap.NewDevelopment()
	defer logger.Sync()

	if err != nil {
		panic(err.Error())
	}

	var specs EnvSpec
	err = envconfig.Process("", &specs)

	if err != nil {
		logger.Sugar().Fatal(err.Error())
	}

	monitor := monCore.NewMonitor(
		monConfig.NewMonitorConfig("web", nil, logger.Sugar()),
	)

	apiCfg := config.NewAPIConfig(
		"web",
		nil,
		trace.NewNoopTracerProvider().Tracer("nop"),
		monitor,
		logger.Sugar(),
	)

	api := core.NewAPI(apiCfg)

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	api.RegisterBlueprints(api.Router(), NewBlueprint(specs.ConfigMap, clientset))

	srv := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%s", specs.Port),
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,
		Handler:      api.Handler(),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			logger.Sugar().Fatal(err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Block until we receive our signal.
	<-c

	// Create a deadline to wait for.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Doesn't block if no connections, but will otherwise wait
	// until the timeout deadline.
	srv.Shutdown(ctx)
	// Optionally, you could run srv.Shutdown in a goroutine and block on
	// <-ctx.Done() if your application should wait for other services
	// to finalize based on context cancellation.
	logger.Sugar().Info("Shutting down")
	os.Exit(0)
}
