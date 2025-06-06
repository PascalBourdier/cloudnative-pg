/*
Copyright © contributors to CloudNativePG, established as
CloudNativePG a Series of LF Projects, LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

SPDX-License-Identifier: Apache-2.0
*/

// Package specs contains the specification of the K8s resources
// generated by the CloudNativePG operator
package specs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"reflect"
	"slices"
	"strconv"

	"github.com/cloudnative-pg/machinery/pkg/log"
	jsonpatch "github.com/evanphx/json-patch/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/cnpi/plugin"
	cnpgiClient "github.com/cloudnative-pg/cloudnative-pg/internal/cnpi/plugin/client"
	"github.com/cloudnative-pg/cloudnative-pg/internal/configuration"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/management/url"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils/hash"
)

const (
	// ClusterSerialAnnotationName is the name of the annotation containing the
	// serial number of the node
	ClusterSerialAnnotationName = utils.ClusterSerialAnnotationName

	// ClusterRestartAnnotationName is the name of the annotation containing the
	// latest required restart time
	ClusterRestartAnnotationName = utils.ClusterRestartAnnotationName

	// ClusterReloadAnnotationName is the name of the annotation containing the
	// latest required restart time
	ClusterReloadAnnotationName = utils.ClusterReloadAnnotationName

	// WatchedLabelName label is for Secrets or ConfigMaps that needs to be reloaded
	WatchedLabelName = utils.WatchedLabelName

	// ClusterRoleLabelPrimary is written in labels to represent primary servers
	ClusterRoleLabelPrimary = "primary"

	// ClusterRoleLabelReplica is written in labels to represent replica servers
	ClusterRoleLabelReplica = "replica"

	// PostgresContainerName is the name of the container executing PostgreSQL
	// inside one Pod
	PostgresContainerName = "postgres"

	// BootstrapControllerContainerName is the name of the container copying the bootstrap
	// controller inside the Pod file system
	BootstrapControllerContainerName = "bootstrap-controller"

	// PgDataPath is the path to PGDATA variable
	PgDataPath = "/var/lib/postgresql/data/pgdata"

	// PgWalPath is the path to the pg_wal directory
	PgWalPath = PgDataPath + "/pg_wal"

	// PgWalArchiveStatusPath is the path to the archive status directory
	PgWalArchiveStatusPath = PgWalPath + "/archive_status"

	// ReadinessProbePeriod is the period set for the postgres instance readiness probe
	ReadinessProbePeriod = 10

	// StartupProbePeriod is the period set for the postgres instance startup probe
	StartupProbePeriod = 10

	// LivenessProbePeriod is the period set for the postgres instance liveness probe
	LivenessProbePeriod = 10
)

// EnvConfig carries the environment configuration of a container
type EnvConfig struct {
	EnvVars []corev1.EnvVar
	EnvFrom []corev1.EnvFromSource
	Hash    string
}

// IsEnvEqual detects if the environment of a container matches
func (c EnvConfig) IsEnvEqual(container corev1.Container) bool {
	// Step 1: detect changes in the envFrom section
	if !slices.EqualFunc(container.EnvFrom, c.EnvFrom, func(e1, e2 corev1.EnvFromSource) bool {
		return reflect.DeepEqual(e1, e2)
	}) {
		return false
	}

	// Step 2: detect changes in the env section
	return slices.EqualFunc(container.Env, c.EnvVars, func(e1, e2 corev1.EnvVar) bool {
		return reflect.DeepEqual(e1, e2)
	})
}

// CreatePodEnvConfig returns the hash of pod env configuration
func CreatePodEnvConfig(cluster apiv1.Cluster, podName string) EnvConfig {
	// When adding an environment variable here, remember to change the `isReservedEnvironmentVariable`
	// function in `cluster_webhook.go` too.
	config := EnvConfig{
		EnvVars: []corev1.EnvVar{
			{
				Name:  "PGDATA",
				Value: PgDataPath,
			},
			{
				Name:  "POD_NAME",
				Value: podName,
			},
			{
				Name:  "NAMESPACE",
				Value: cluster.Namespace,
			},
			{
				Name:  "CLUSTER_NAME",
				Value: cluster.Name,
			},
			{
				Name:  "PSQL_HISTORY",
				Value: path.Join(postgres.TemporaryDirectory, ".psql_history"),
			},
			{
				Name:  "PGPORT",
				Value: strconv.Itoa(postgres.ServerPort),
			},
			{
				Name:  "PGHOST",
				Value: postgres.SocketDirectory,
			},
			{
				Name:  "TMPDIR",
				Value: postgres.TemporaryDirectory,
			},
		},
		EnvFrom: cluster.Spec.EnvFrom,
	}
	config.EnvVars = append(config.EnvVars, cluster.Spec.Env...)

	if configuration.Current.StandbyTCPUserTimeout != 0 {
		config.EnvVars = append(
			config.EnvVars,
			corev1.EnvVar{
				Name:  "CNPG_STANDBY_TCP_USER_TIMEOUT",
				Value: strconv.Itoa(configuration.Current.StandbyTCPUserTimeout),
			},
		)
	}

	hashValue, _ := hash.ComputeHash(config)
	config.Hash = hashValue
	return config
}

// createClusterPodSpec computes the PodSpec corresponding to a cluster
func createClusterPodSpec(
	podName string,
	cluster apiv1.Cluster,
	envConfig EnvConfig,
	gracePeriod int64,
	enableHTTPS bool,
) corev1.PodSpec {
	return corev1.PodSpec{
		Hostname: podName,
		InitContainers: []corev1.Container{
			createBootstrapContainer(cluster),
		},
		SchedulerName: cluster.Spec.SchedulerName,
		Containers:    createPostgresContainers(cluster, envConfig, enableHTTPS),
		Volumes:       createPostgresVolumes(&cluster, podName),
		SecurityContext: CreatePodSecurityContext(
			cluster.GetSeccompProfile(),
			cluster.GetPostgresUID(),
			cluster.GetPostgresGID()),
		Affinity:                      CreateAffinitySection(cluster.Name, cluster.Spec.Affinity),
		Tolerations:                   cluster.Spec.Affinity.Tolerations,
		ServiceAccountName:            cluster.Name,
		NodeSelector:                  cluster.Spec.Affinity.NodeSelector,
		TerminationGracePeriodSeconds: &gracePeriod,
		TopologySpreadConstraints:     cluster.Spec.TopologySpreadConstraints,
	}
}

// createPostgresContainers create the PostgreSQL containers that are
// used for every instance
func createPostgresContainers(cluster apiv1.Cluster, envConfig EnvConfig, enableHTTPS bool) []corev1.Container {
	containers := []corev1.Container{
		{
			Name:            PostgresContainerName,
			Image:           cluster.Status.Image,
			ImagePullPolicy: cluster.Spec.ImagePullPolicy,
			Env:             envConfig.EnvVars,
			EnvFrom:         envConfig.EnvFrom,
			VolumeMounts:    CreatePostgresVolumeMounts(cluster),
			// This is the default startup probe, and can be overridden
			// the user configuration in cluster.spec.probes.startup
			StartupProbe: &corev1.Probe{
				PeriodSeconds:  StartupProbePeriod,
				TimeoutSeconds: 5,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: url.PathStartup,
						Port: intstr.FromInt32(url.StatusPort),
					},
				},
			},
			// This is the default readiness probe, and can be overridden
			// by the user configuration in cluster.spec.probes.readiness
			ReadinessProbe: &corev1.Probe{
				TimeoutSeconds: 5,
				PeriodSeconds:  ReadinessProbePeriod,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: url.PathReady,
						Port: intstr.FromInt32(url.StatusPort),
					},
				},
			},
			// This is the default liveness probe, and can be overridden
			// by the user configuration in cluster.spec.probes.liveness
			LivenessProbe: &corev1.Probe{
				PeriodSeconds:  LivenessProbePeriod,
				TimeoutSeconds: 5,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: url.PathHealth,
						Port: intstr.FromInt32(url.StatusPort),
					},
				},
			},
			Command: []string{
				"/controller/manager",
				"instance",
				"run",
			},
			Resources: cluster.Spec.Resources,
			Ports: []corev1.ContainerPort{
				{
					Name:          "postgresql",
					ContainerPort: postgres.ServerPort,
					Protocol:      "TCP",
				},
				{
					Name:          "metrics",
					ContainerPort: url.PostgresMetricsPort,
					Protocol:      "TCP",
				},
				{
					Name:          "status",
					ContainerPort: url.StatusPort,
					Protocol:      "TCP",
				},
			},
			SecurityContext: CreateContainerSecurityContext(cluster.GetSeccompProfile()),
		},
	}

	if enableHTTPS {
		containers[0].StartupProbe.ProbeHandler.HTTPGet.Scheme = corev1.URISchemeHTTPS
		containers[0].LivenessProbe.ProbeHandler.HTTPGet.Scheme = corev1.URISchemeHTTPS
		containers[0].ReadinessProbe.ProbeHandler.HTTPGet.Scheme = corev1.URISchemeHTTPS
		containers[0].Command = append(containers[0].Command, "--status-port-tls")
	}

	if cluster.IsMetricsTLSEnabled() {
		containers[0].Command = append(containers[0].Command, "--metrics-port-tls")
	}

	addManagerLoggingOptions(cluster, &containers[0])

	// use the custom probe configuration if provided
	ensureCustomProbesConfiguration(&cluster, &containers[0])

	// ensure a proper threshold is set
	if containers[0].StartupProbe.FailureThreshold == 0 {
		containers[0].StartupProbe.FailureThreshold = getFailureThreshold(
			cluster.GetMaxStartDelay(),
			containers[0].StartupProbe.PeriodSeconds,
		)
	}

	if cluster.Spec.LivenessProbeTimeout != nil && containers[0].LivenessProbe.FailureThreshold == 0 {
		containers[0].LivenessProbe.FailureThreshold = getFailureThreshold(
			*cluster.Spec.LivenessProbeTimeout,
			containers[0].LivenessProbe.PeriodSeconds,
		)
	}

	return containers
}

// ensureCustomProbesConfiguration applies the custom probe configuration
// if specified inside the cluster specification
func ensureCustomProbesConfiguration(cluster *apiv1.Cluster, container *corev1.Container) {
	// No probes configuration
	if cluster.Spec.Probes == nil {
		return
	}

	// There's no need to check for nils here because a nil probe specification
	// will result in no change in the Kubernetes probe.
	cluster.Spec.Probes.Liveness.ApplyInto(container.LivenessProbe)
	cluster.Spec.Probes.Readiness.ApplyInto(container.ReadinessProbe)
	cluster.Spec.Probes.Startup.ApplyInto(container.StartupProbe)
}

// getFailureThreshold get the startup probe failure threshold
// FAILURE_THRESHOLD = ceil(startDelay / periodSeconds) and minimum value is 1
func getFailureThreshold(startupDelay, period int32) int32 {
	if startupDelay <= period {
		return 1
	}
	return int32(math.Ceil(float64(startupDelay) / float64(period)))
}

// CreateAffinitySection creates the affinity sections for Pods, given the configuration
// from the user
func CreateAffinitySection(clusterName string, config apiv1.AffinityConfiguration) *corev1.Affinity {
	// Initialize affinity
	affinity := CreateGeneratedAntiAffinity(clusterName, config)

	if config.AdditionalPodAffinity == nil &&
		config.AdditionalPodAntiAffinity == nil &&
		config.NodeAffinity == nil {
		return affinity
	}

	if affinity == nil {
		affinity = &corev1.Affinity{}
	}

	if config.AdditionalPodAffinity != nil {
		affinity.PodAffinity = config.AdditionalPodAffinity
	}

	if config.AdditionalPodAntiAffinity != nil {
		if affinity.PodAntiAffinity == nil {
			affinity.PodAntiAffinity = &corev1.PodAntiAffinity{}
		}
		affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
			affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			config.AdditionalPodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution...)
		affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			config.AdditionalPodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution...)
	}

	if config.NodeAffinity != nil {
		affinity.NodeAffinity = config.NodeAffinity
	}

	return affinity
}

// CreateGeneratedAntiAffinity generates the affinity terms the operator is in charge for if enabled,
// return nil if disabled or an error occurred, as invalid values should be validated before this method is called
func CreateGeneratedAntiAffinity(clusterName string, config apiv1.AffinityConfiguration) *corev1.Affinity {
	// We have no anti affinity section if the user don't have it configured
	if config.EnablePodAntiAffinity != nil && !(*config.EnablePodAntiAffinity) {
		return nil
	}
	affinity := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
	topologyKey := config.TopologyKey
	if len(topologyKey) == 0 {
		topologyKey = "kubernetes.io/hostname"
	}

	podAffinityTerm := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      utils.ClusterLabelName,
					Operator: metav1.LabelSelectorOpIn,
					Values: []string{
						clusterName,
					},
				},
				{
					Key:      utils.PodRoleLabelName,
					Operator: metav1.LabelSelectorOpIn,
					Values: []string{
						string(utils.PodRoleInstance),
					},
				},
			},
		},
		TopologyKey: topologyKey,
	}

	// Switch pod anti-affinity type:
	// - if it is "required", 'RequiredDuringSchedulingIgnoredDuringExecution' will be properly set.
	// - if it is "preferred",'PreferredDuringSchedulingIgnoredDuringExecution' will be properly set.
	// - by default, return nil.
	switch config.PodAntiAffinityType {
	case apiv1.PodAntiAffinityTypeRequired:
		affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{
			podAffinityTerm,
		}
	case apiv1.PodAntiAffinityTypePreferred:
		affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{
			{
				Weight:          100,
				PodAffinityTerm: podAffinityTerm,
			},
		}
	default:
		return nil
	}
	return affinity
}

// CreatePodSecurityContext defines the security context under which the containers are running
func CreatePodSecurityContext(seccompProfile *corev1.SeccompProfile, user, group int64) *corev1.PodSecurityContext {
	// Under Openshift we inherit SecurityContext from the restricted security context constraint
	if utils.HaveSecurityContextConstraints() {
		return nil
	}

	trueValue := true
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   &trueValue,
		RunAsUser:      &user,
		RunAsGroup:     &group,
		FSGroup:        &group,
		SeccompProfile: seccompProfile,
	}
}

// NewInstance creates a new instance Pod with the plugin patches applied
func NewInstance(
	ctx context.Context,
	cluster apiv1.Cluster,
	nodeSerial int,
	// tlsEnabled TODO: remove when we drop the support for the instances created without TLS
	tlsEnabled bool,
) (*corev1.Pod, error) {
	contextLogger := log.FromContext(ctx).WithName("new_instance")

	pod, err := buildInstance(cluster, nodeSerial, tlsEnabled)
	if err != nil {
		return nil, err
	}

	defer func() {
		if pod == nil {
			return
		}
		if podSpecMarshaled, marshalErr := json.Marshal(pod.Spec); marshalErr == nil {
			pod.Annotations[utils.PodSpecAnnotationName] = string(podSpecMarshaled)
		}
	}()

	pluginClient := cnpgiClient.GetPluginClientFromContext(ctx)
	if pluginClient == nil {
		contextLogger.Trace("skipping NewInstance, cannot find the plugin client inside the context")
		return pod, nil
	}

	contextLogger.Trace("correctly loaded the plugin client for instance evaluation")

	podClientObject, err := pluginClient.LifecycleHook(ctx, plugin.OperationVerbEvaluate, &cluster, pod)
	if err != nil {
		return nil, fmt.Errorf("while invoking the lifecycle instance evaluation hook: %w", err)
	}

	var ok bool
	pod, ok = podClientObject.(*corev1.Pod)
	if !ok {
		return nil, fmt.Errorf("while casting the clientObject to the pod type")
	}

	return pod, nil
}

func buildInstance(
	cluster apiv1.Cluster,
	nodeSerial int,
	tlsEnabled bool,
) (*corev1.Pod, error) {
	podName := GetInstanceName(cluster.Name, nodeSerial)
	gracePeriod := int64(cluster.GetMaxStopDelay())

	envConfig := CreatePodEnvConfig(cluster, podName)

	podSpec := createClusterPodSpec(podName, cluster, envConfig, gracePeriod, tlsEnabled)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				utils.ClusterLabelName:      cluster.Name,
				utils.InstanceNameLabelName: podName,
				utils.PodRoleLabelName:      string(utils.PodRoleInstance),
			},
			Annotations: map[string]string{
				utils.ClusterSerialAnnotationName: strconv.Itoa(nodeSerial),
				utils.PodEnvHashAnnotationName:    envConfig.Hash,
			},
			Name:      podName,
			Namespace: cluster.Namespace,
		},
		Spec: podSpec,
	}

	if cluster.Spec.PriorityClassName != "" {
		pod.Spec.PriorityClassName = cluster.Spec.PriorityClassName
	}

	if configuration.Current.CreateAnyService {
		pod.Spec.Subdomain = cluster.GetServiceAnyName()
	}

	if utils.IsAnnotationAppArmorPresent(&pod.Spec, cluster.Annotations) {
		utils.AnnotateAppArmor(&pod.ObjectMeta, &pod.Spec, cluster.Annotations)
	}

	if jsonPatch := cluster.Annotations[utils.PodPatchAnnotationName]; jsonPatch != "" {
		serializedObject, err := json.Marshal(pod)
		if err != nil {
			return nil, fmt.Errorf("while serializing pod to JSON: %w", err)
		}
		patch, err := jsonpatch.DecodePatch([]byte(jsonPatch))
		if err != nil {
			return nil, fmt.Errorf("while decoding JSON patch from annotation: %w", err)
		}

		serializedObject, err = patch.Apply(serializedObject)
		if err != nil {
			return nil, fmt.Errorf("while applying JSON patch from annotation: %w", err)
		}

		if err = json.Unmarshal(serializedObject, pod); err != nil {
			return nil, fmt.Errorf("while deserializing pod to JSON: %w", err)
		}
	}

	return pod, nil
}

// GetInstanceName returns a string indicating the instance name
func GetInstanceName(clusterName string, nodeSerial int) string {
	return fmt.Sprintf("%s-%v", clusterName, nodeSerial)
}

// AddBarmanEndpointCAToPodSpec adds the required volumes and env variables needed by barman to work correctly
func AddBarmanEndpointCAToPodSpec(
	podSpec *corev1.PodSpec,
	caSecret *apiv1.SecretKeySelector,
	credentials apiv1.BarmanCredentials,
) {
	if caSecret == nil || caSecret.Name == "" || caSecret.Key == "" {
		return
	}

	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: "barman-endpoint-ca",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: caSecret.Name,
				Items: []corev1.KeyToPath{
					{
						Key:  caSecret.Key,
						Path: postgres.BarmanRestoreEndpointCACertificateFileName,
					},
				},
			},
		},
	})

	podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts,
		corev1.VolumeMount{
			Name:      "barman-endpoint-ca",
			MountPath: postgres.CertificatesDir,
		},
	)

	var envVars []corev1.EnvVar
	// todo: add a case for the Google provider
	switch {
	case credentials.Azure != nil:
		envVars = append(envVars, corev1.EnvVar{
			Name:  "REQUESTS_CA_BUNDLE",
			Value: postgres.BarmanRestoreEndpointCACertificateLocation,
		})
	// If nothing is set we fall back to AWS, this is to avoid breaking changes with previous versions
	default:
		envVars = append(envVars, corev1.EnvVar{
			Name:  "AWS_CA_BUNDLE",
			Value: postgres.BarmanRestoreEndpointCACertificateLocation,
		})
	}

	podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, envVars...)
}
