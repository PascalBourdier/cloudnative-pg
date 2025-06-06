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

package backup

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	pgTime "github.com/cloudnative-pg/machinery/pkg/postgres/time"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/internal/cmd/plugin"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
)

// backupCommandOptions are the options that are provider to the backup
// cnpg command
type backupCommandOptions struct {
	backupName          string
	clusterName         string
	target              apiv1.BackupTarget
	method              apiv1.BackupMethod
	online              *bool
	immediateCheckpoint *bool
	waitForArchive      *bool
	pluginName          string
	pluginParameters    pluginParameters
}

func (options backupCommandOptions) getOnlineConfiguration() *apiv1.OnlineConfiguration {
	var onlineConfiguration *apiv1.OnlineConfiguration
	if options.immediateCheckpoint != nil || options.waitForArchive != nil {
		onlineConfiguration = &apiv1.OnlineConfiguration{
			WaitForArchive:      options.waitForArchive,
			ImmediateCheckpoint: options.immediateCheckpoint,
		}
	}
	return onlineConfiguration
}

// NewCmd creates the new "backup" subcommand
func NewCmd() *cobra.Command {
	var backupName, backupTarget, backupMethod, online, immediateCheckpoint, waitForArchive, pluginName string
	var pluginParameters pluginParameters

	backupMethods := []string{
		string(apiv1.BackupMethodBarmanObjectStore),
		string(apiv1.BackupMethodVolumeSnapshot),
		string(apiv1.BackupMethodPlugin),
	}

	backupSubcommand := &cobra.Command{
		Use:     "backup CLUSTER",
		Short:   "Request an on-demand backup for a PostgreSQL Cluster",
		GroupID: plugin.GroupIDDatabase,
		Args:    plugin.RequiresArguments(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return plugin.CompleteClusters(cmd.Context(), args, toComplete), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterName := args[0]

			if len(backupName) == 0 {
				backupName = fmt.Sprintf(
					"%s-%s",
					clusterName,
					pgTime.ToCompactISO8601(time.Now()),
				)
			}

			// Check if the backup target is correct
			allowedBackupTargets := []string{
				"",
				string(apiv1.BackupTargetPrimary),
				string(apiv1.BackupTargetStandby),
			}
			if !slices.Contains(allowedBackupTargets, backupTarget) {
				return fmt.Errorf("backup-target: %s is not supported by the backup command", backupTarget)
			}

			// Check if the backup method is correct
			allowedBackupMethods := backupMethods
			allowedBackupMethods = append(allowedBackupMethods, "")
			if !slices.Contains(allowedBackupMethods, backupMethod) {
				return fmt.Errorf("backup-method: %s is not supported by the backup command", backupMethod)
			}

			if backupMethod == string(apiv1.BackupMethodPlugin) {
				if len(pluginName) == 0 {
					return fmt.Errorf("plugin-name is required when backup method in %s",
						apiv1.BackupMethodPlugin)
				}
			} else {
				if len(pluginName) > 0 {
					return fmt.Errorf("plugin-name is allowed only when backup method in %s",
						apiv1.BackupMethodPlugin)
				}

				if len(pluginParameters) > 0 {
					return fmt.Errorf("plugin-parameters is allowed only when backup method in %s",
						apiv1.BackupMethodPlugin)
				}
			}

			var cluster apiv1.Cluster
			// check if the cluster exists
			err := plugin.Client.Get(
				cmd.Context(),
				client.ObjectKey{
					Namespace: plugin.Namespace,
					Name:      clusterName,
				},
				&cluster,
			)
			if err != nil {
				return fmt.Errorf("while getting cluster %s: %w", clusterName, err)
			}

			parsedOnline, err := parseOptionalBooleanString(online)
			if err != nil {
				return fmt.Errorf("while parsing the online value: %w", err)
			}
			parsedImmediateCheckpoint, err := parseOptionalBooleanString(immediateCheckpoint)
			if err != nil {
				return fmt.Errorf("while parsing the immediate-checkpoint value: %w", err)
			}
			parsedWaitForArchive, err := parseOptionalBooleanString(waitForArchive)
			if err != nil {
				return fmt.Errorf("while parsing the wait-for-archive value: %w", err)
			}

			return createBackup(
				cmd.Context(),
				backupCommandOptions{
					backupName:          backupName,
					clusterName:         clusterName,
					target:              apiv1.BackupTarget(backupTarget),
					method:              apiv1.BackupMethod(backupMethod),
					online:              parsedOnline,
					immediateCheckpoint: parsedImmediateCheckpoint,
					waitForArchive:      parsedWaitForArchive,
					pluginName:          pluginName,
					pluginParameters:    pluginParameters,
				})
		},
	}

	backupSubcommand.Flags().StringVar(
		&backupName,
		"backup-name",
		"",
		"The name of the Backup resource that will be created, "+
			"defaults to \"CLUSTER-CURRENT_TIMESTAMP\"",
	)
	backupSubcommand.Flags().StringVarP(
		&backupTarget,
		"backup-target",
		"t",
		"",
		"If present, will override the backup target defined in cluster, "+
			"valid values are primary and prefer-standby.",
	)
	backupSubcommand.Flags().StringVarP(
		&backupMethod,
		"method",
		"m",
		"",
		fmt.Sprintf("If present, will override the backup method defined in backup resource, "+
			"valid values are: %s.", strings.Join(backupMethods, ", ")),
	)

	const optionalAcceptedValues = "Optional. Accepted values: true|false|\"\"."
	backupSubcommand.Flags().StringVar(&online, "online",
		"",
		"Set the '.spec.online' field of the Backup resource. If not specified, "+
			"the value in the '.spec.backup.volumeSnapshot' field of the Cluster "+
			"resource will be used. "+
			optionalAcceptedValues)

	backupSubcommand.Flags().StringVar(&immediateCheckpoint, "immediate-checkpoint", "",
		"Set the '.spec.onlineConfiguration.immediateCheckpoint' field of the "+
			"Backup resource. If not specified, the value in the "+
			"'.spec.backup.volumeSnapshot.onlineConfiguration' field "+
			"of the Cluster resource will be used. "+
			optionalAcceptedValues,
	)

	backupSubcommand.Flags().StringVar(&waitForArchive, "wait-for-archive", "",
		"Set the '.spec.onlineConfiguratoin.waitForArchive' field of the "+
			"Backup resource. If not specified, the value in the "+
			"'.spec.backup.volumeSnapshot.onlineConfiguration' field will be used. "+
			optionalAcceptedValues,
	)

	backupSubcommand.Flags().StringVar(&pluginName, "plugin-name", "",
		"The name of the plugin that should take the backup. This option "+
			"is allowed only when the backup method is set to 'plugin'",
	)

	backupSubcommand.Flags().VarP(&pluginParameters, "plugin-parameters", "",
		"The set of plugin parameters that should be passed to the backup plugin "+
			" i.e. param-one=value,param-two=value. This option "+
			"is allowed only when the backup method is set to 'plugin'",
	)

	return backupSubcommand
}

// createBackup handles the Backup resource creation
func createBackup(ctx context.Context, options backupCommandOptions) error {
	backup := apiv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: plugin.Namespace,
			Name:      options.backupName,
		},
		Spec: apiv1.BackupSpec{
			Cluster: apiv1.LocalObjectReference{
				Name: options.clusterName,
			},
			Target:              options.target,
			Method:              options.method,
			Online:              options.online,
			OnlineConfiguration: options.getOnlineConfiguration(),
		},
	}
	utils.LabelClusterName(&backup.ObjectMeta, options.clusterName)

	if len(options.pluginName) > 0 {
		backup.Spec.PluginConfiguration = &apiv1.BackupPluginConfiguration{
			Name:       options.pluginName,
			Parameters: options.pluginParameters,
		}
	}

	err := plugin.Client.Create(ctx, &backup)
	if err == nil {
		fmt.Printf("backup/%v created\n", backup.Name)
	}
	return err
}

func parseOptionalBooleanString(rawBool string) (*bool, error) {
	if rawBool == "" {
		return nil, nil
	}

	value, err := strconv.ParseBool(rawBool)
	if err != nil {
		return nil, err
	}
	return ptr.To(value), nil
}
