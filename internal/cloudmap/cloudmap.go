package cloudmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
)

const (
	heartbeatInterval = 10 * time.Second
	healthCheckURL    = "http://localhost:8080/healthz"
)

// Manager handles AWS Cloud Map registration and health heartbeats
type Manager struct {
	serviceID  string
	instanceID string
	privateIP  string
	region     string
	client     *servicediscovery.Client
	logger     *slog.Logger

	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	healthCheckDisabled bool
}

// New creates a Cloud Map manager. It fetches EC2 instance metadata and registers with Cloud Map.
func New(ctx context.Context, serviceID string, logger *slog.Logger) (*Manager, error) {
	// Load AWS config
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	// Get instance metadata
	imdsClient := imds.NewFromConfig(cfg)

	instanceID, err := getInstanceID(ctx, imdsClient)
	if err != nil {
		return nil, fmt.Errorf("get instance id: %w", err)
	}

	privateIP, err := getPrivateIP(ctx, imdsClient)
	if err != nil {
		return nil, fmt.Errorf("get private ip: %w", err)
	}

	region, err := getRegion(ctx, imdsClient)
	if err != nil {
		return nil, fmt.Errorf("get region: %w", err)
	}

	// Reload config with region
	cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config with region: %w", err)
	}

	m := &Manager{
		serviceID:  serviceID,
		instanceID: instanceID,
		privateIP:  privateIP,
		region:     region,
		client:     servicediscovery.NewFromConfig(cfg),
		logger:     logger,
	}

	return m, nil
}

// Start registers the instance with Cloud Map and begins the health heartbeat loop.
func (m *Manager) Start(ctx context.Context) error {
	// Register instance
	output, err := m.client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:        aws.String(m.serviceID),
		InstanceId:       aws.String(m.instanceID),
		CreatorRequestId: aws.String(m.instanceID),
		Attributes: map[string]string{
			"AWS_INSTANCE_IPV4":      m.privateIP,
			"AWS_INIT_HEALTH_STATUS": string(sdtypes.CustomHealthStatusUnhealthy),
		},
	})
	if err != nil {
		return fmt.Errorf("register instance: %w", err)
	}

	m.logger.Info("registered with cloud map",
		"operation_id", output.OperationId,
		"service_id", m.serviceID,
		"instance_id", m.instanceID,
		"private_ip", m.privateIP,
		"region", m.region,
	)

	// Start heartbeat with delay for eventual consistency
	hbCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.wg.Add(1)
	go func() {
		// Wait for Cloud Map registration to propagate before health updates
		time.Sleep(5 * time.Second)
		m.heartbeatLoop(hbCtx)
	}()

	return nil
}

// Stop stops the heartbeat loop and deregisters from Cloud Map.
func (m *Manager) Stop(ctx context.Context) {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()

	// Deregister on shutdown
	_, err := m.client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId:  aws.String(m.serviceID),
		InstanceId: aws.String(m.instanceID),
	})
	if err != nil {
		m.logger.Error("failed to deregister from cloud map", "err", err)
	} else {
		m.logger.Info("deregistered from cloud map", "instance_id", m.instanceID)
	}
}

func (m *Manager) heartbeatLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	// Initial health check
	m.updateHealthStatus(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.updateHealthStatus(ctx)
		}
	}
}

func (m *Manager) updateHealthStatus(ctx context.Context) {
	if m.healthCheckDisabled {
		return
	}

	status := sdtypes.CustomHealthStatusHealthy
	if !m.checkHealth() {
		status = sdtypes.CustomHealthStatusUnhealthy
	}

	_, err := m.client.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId:  aws.String(m.serviceID),
		InstanceId: aws.String(m.instanceID),
		Status:     status,
	})
	if err != nil {
		m.logger.Warn("failed to update cloud map health status", "err", err, "status", status)
	} else {
		m.logger.Debug("updated cloud map health status", "status", status)
	}
}

func (m *Manager) checkHealth() bool {
	resp, err := http.Get(healthCheckURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func getInstanceID(ctx context.Context, client *imds.Client) (string, error) {
	output, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "instance-id",
	})
	if err != nil {
		return "", err
	}
	defer output.Content.Close()
	b, err := io.ReadAll(output.Content)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getPrivateIP(ctx context.Context, client *imds.Client) (string, error) {
	output, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "local-ipv4",
	})
	if err != nil {
		return "", err
	}
	defer output.Content.Close()
	b, err := io.ReadAll(output.Content)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getRegion(ctx context.Context, client *imds.Client) (string, error) {
	output, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "placement/region",
	})
	if err != nil {
		// Fallback to document
		return getRegionFromDocument(ctx, client)
	}
	defer output.Content.Close()
	b, err := io.ReadAll(output.Content)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getRegionFromDocument(ctx context.Context, client *imds.Client) (string, error) {
	output, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "dynamic/instance-identity/document",
	})
	if err != nil {
		return "", err
	}
	defer output.Content.Close()
	var doc struct {
		Region string `json:"region"`
	}
	if err := json.NewDecoder(output.Content).Decode(&doc); err != nil {
		return "", err
	}
	return doc.Region, nil
}
