package monitoring

import (
	"reflect"

	"github.com/pkg/errors"
	"github.com/rancher/rancher/pkg/monitoring"
	"github.com/rancher/rancher/pkg/settings"
	mgmtv3 "github.com/rancher/types/apis/management.cattle.io/v3"
	projectv3 "github.com/rancher/types/apis/project.cattle.io/v3"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type operatorHandler struct {
	clusterName         string
	cattleClusterClient mgmtv3.ClusterInterface
	app                 *appHandler
}

func (h *operatorHandler) sync(key string, obj *mgmtv3.Cluster) (runtime.Object, error) {
	if obj == nil || obj.DeletionTimestamp != nil || obj.Name != h.clusterName {
		return obj, nil
	}
	var newCluster *mgmtv3.Cluster
	var err error
	//should deploy
	if obj.Spec.EnableClusterAlerting || obj.Spec.EnableClusterMonitoring {
		newObj, err := mgmtv3.ClusterConditionPrometheusOperatorDeployed.DoUntilTrue(obj, func() (runtime.Object, error) {
			cpy := obj.DeepCopy()
			return cpy, deploySystemMonitor(cpy, h.app)
		})
		if err != nil {
			logrus.WithError(err).Info("sync cluster monitor operator error")
		}
		newCluster = newObj.(*mgmtv3.Cluster)
	} else { // should withdraw
		//status false and empty should withdraw. when status unknown, it means the deployment has error while deploying apps
		if (mgmtv3.MonitoringConditionAlertmaanagerDeployed.IsFalse(obj) ||
			mgmtv3.MonitoringConditionAlertmaanagerDeployed.GetStatus(obj) == "") &&
			(mgmtv3.ClusterConditionMonitoringEnabled.IsFalse(obj) ||
				mgmtv3.ClusterConditionMonitoringEnabled.GetStatus(obj) == "") &&
			!mgmtv3.ClusterConditionPrometheusOperatorDeployed.IsFalse(obj) {
			newCluster = obj.DeepCopy()
			if err = withdrawSystemMonitor(h.app, newCluster.Name); err != nil {
				mgmtv3.ClusterConditionPrometheusOperatorDeployed.Unknown(newCluster)
				mgmtv3.ClusterConditionPrometheusOperatorDeployed.ReasonAndMessageFromError(newCluster, err)
			} else {
				mgmtv3.ClusterConditionPrometheusOperatorDeployed.False(newCluster)
			}
		}
	}

	if newCluster != nil && !reflect.DeepEqual(newCluster, obj) {
		if newCluster, err = h.cattleClusterClient.Update(newCluster); err != nil {
			return nil, err
		}
		return newCluster, nil
	}
	return obj, nil
}

func deploySystemMonitor(cluster *mgmtv3.Cluster, app *appHandler) (backErr error) {
	appName, appTargetNamespace := monitoring.SystemMonitoringInfo()

	appCatalogID := settings.SystemMonitoringCatalogID.Get()
	err := monitoring.DetectAppCatalogExistence(appCatalogID, app.cattleTemplateVersionClient)
	if err != nil {
		return errors.Wrapf(err, "failed to ensure catalog %q", appCatalogID)
	}

	appDeployProjectID, err := monitoring.GetSystemProjectID(app.cattleProjectClient)
	if err != nil {
		return errors.Wrap(err, "failed to get System Project ID")
	}

	appProjectName, err := monitoring.EnsureAppProjectName(app.agentNamespaceClient, appDeployProjectID, cluster.Name, appTargetNamespace)
	if err != nil {
		return errors.Wrap(err, "failed to ensure monitoring project name")
	}

	appAnswers := map[string]string{
		"enabled":      "true",
		"apiGroup":     monitoring.APIVersion.Group,
		"nameOverride": "prometheus-operator",
	}
	annotations := monitoring.CopyCreatorID(nil, cluster.Annotations)
	annotations["cluster.cattle.io/addon"] = appName
	targetApp := &projectv3.App{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotations,
			Labels:      monitoring.OwnedLabels(appName, appTargetNamespace, appProjectName, monitoring.SystemLevel),
			Name:        appName,
			Namespace:   appDeployProjectID,
		},
		Spec: projectv3.AppSpec{
			Answers:         appAnswers,
			Description:     "Prometheus Operator for Rancher Monitoring",
			ExternalID:      appCatalogID,
			ProjectName:     appProjectName,
			TargetNamespace: appTargetNamespace,
		},
	}

	err = monitoring.DeployApp(app.cattleAppClient, appDeployProjectID, targetApp)
	if err != nil {
		return errors.Wrap(err, "failed to ensure prometheus operator app")
	}

	return nil
}

func withdrawSystemMonitor(app *appHandler, clusterID string) error {
	appName, appTargetNamespace := monitoring.SystemMonitoringInfo()

	if err := monitoring.WithdrawApp(app.cattleAppClient, monitoring.OwnedAppListOptions(clusterID, appName, appTargetNamespace)); err != nil {
		return errors.Wrap(err, "failed to withdraw prometheus operator app")
	}

	return nil
}
