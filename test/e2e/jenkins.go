package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/jenkinsci/kubernetes-operator/pkg/apis/jenkins/v1alpha2"
	jenkinsclient "github.com/jenkinsci/kubernetes-operator/pkg/client"
	"github.com/jenkinsci/kubernetes-operator/pkg/configuration"
	"github.com/jenkinsci/kubernetes-operator/pkg/configuration/base"
	"github.com/jenkinsci/kubernetes-operator/pkg/configuration/base/resources"
	"github.com/jenkinsci/kubernetes-operator/pkg/constants"

	framework "github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	userConfigurationConfigMapName = "user-config"
	userConfigurationSecretName    = "user-secret"
)

func getJenkins(t *testing.T, namespace, name string) *v1alpha2.Jenkins {
	jenkins := &v1alpha2.Jenkins{}
	namespaceName := types.NamespacedName{Namespace: namespace, Name: name}
	if err := framework.Global.Client.Get(context.TODO(), namespaceName, jenkins); err != nil {
		t.Fatal(err)
	}

	return jenkins
}

func getJenkinsMasterPod(t *testing.T, jenkins *v1alpha2.Jenkins) *corev1.Pod {
	lo := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(resources.GetJenkinsMasterPodLabels(*jenkins)).String(),
	}
	podList, err := framework.Global.KubeClient.CoreV1().Pods(jenkins.ObjectMeta.Namespace).List(lo)
	if err != nil {
		t.Fatal(err)
	}
	if len(podList.Items) != 1 {
		t.Fatalf("Jenkins pod not found, pod list: %+v", podList)
	}
	return &podList.Items[0]
}

func createJenkinsCR(t *testing.T, name, namespace string, seedJob *[]v1alpha2.SeedJob, groovyScripts v1alpha2.GroovyScripts, casc v1alpha2.ConfigurationAsCode, priorityClassName string) *v1alpha2.Jenkins {
	var seedJobs []v1alpha2.SeedJob
	if seedJob != nil {
		seedJobs = append(seedJobs, *seedJob...)
	}

	jenkins := &v1alpha2.Jenkins{
		TypeMeta: v1alpha2.JenkinsTypeMeta(),
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha2.JenkinsSpec{
			GroovyScripts:       groovyScripts,
			ConfigurationAsCode: casc,
			Master: v1alpha2.JenkinsMaster{
				Annotations: map[string]string{"test": "label"},
				Containers: []v1alpha2.Container{
					{
						Name: resources.JenkinsMasterContainerName,
						Env: []corev1.EnvVar{
							{
								Name:  "TEST_ENV",
								Value: "test_env_value",
							},
						},
						ReadinessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Path:   "/login",
									Port:   intstr.FromString("http"),
									Scheme: corev1.URISchemeHTTP,
								},
							},
							InitialDelaySeconds: int32(80),
							TimeoutSeconds:      int32(4),
							FailureThreshold:    int32(10),
						},
						LivenessProbe: &corev1.Probe{
							Handler: corev1.Handler{
								HTTPGet: &corev1.HTTPGetAction{
									Path:   "/login",
									Port:   intstr.FromString("http"),
									Scheme: corev1.URISchemeHTTP,
								},
							},
							InitialDelaySeconds: int32(80),
							TimeoutSeconds:      int32(4),
							FailureThreshold:    int32(10),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "plugins-cache",
								MountPath: "/usr/share/jenkins/ref/plugins",
							},
						},
					},
					{
						Name:  "envoyproxy",
						Image: "envoyproxy/envoy-alpine:v1.14.1",
					},
				},
				Plugins: []v1alpha2.Plugin{
					{Name: "audit-trail", Version: "3.7"},
					{Name: "simple-theme-plugin", Version: "0.6"},
					{Name: "github", Version: "1.32.0"},
					{Name: "devoptics", Version: "1.1905", DownloadURL: "https://jenkins-updates.cloudbees.com/download/plugins/devoptics/1.1905/devoptics.hpi"},
				},
				PriorityClassName: priorityClassName,
				NodeSelector:      map[string]string{"kubernetes.io/os": "linux"},
				Volumes: []corev1.Volume{
					{
						Name: "plugins-cache",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
			SeedJobs: seedJobs,
			Service: v1alpha2.Service{
				Type: corev1.ServiceTypeNodePort,
				Port: constants.DefaultHTTPPortInt32,
			},
		},
	}
	jenkins.Spec.Roles = []rbacv1.RoleRef{
		{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     resources.GetResourceName(jenkins),
		},
	}
	updateJenkinsCR(t, jenkins)

	t.Logf("Jenkins CR %+v", *jenkins)
	if err := framework.Global.Client.Create(context.TODO(), jenkins, nil); err != nil {
		t.Fatal(err)
	}

	return jenkins
}

func createJenkinsAPIClientFromServiceAccount(t *testing.T, jenkins *v1alpha2.Jenkins, jenkinsAPIURL string) (jenkinsclient.Jenkins, error) {
	t.Log("Creating Jenkins API client from service account")
	podName := resources.GetJenkinsMasterPodName(jenkins)

	clientSet, err := kubernetes.NewForConfig(framework.Global.KubeConfig)
	if err != nil {
		return nil, err
	}
	config := configuration.Configuration{Jenkins: jenkins, ClientSet: *clientSet, Config: framework.Global.KubeConfig}
	r := base.New(config, jenkinsclient.JenkinsAPIConnectionSettings{})

	token, _, err := r.Configuration.Exec(podName, resources.JenkinsMasterContainerName, []string{"cat", "/var/run/secrets/kubernetes.io/serviceaccount/token"})
	if err != nil {
		return nil, err
	}

	return jenkinsclient.NewBearerTokenAuthorization(jenkinsAPIURL, token.String())
}

func createJenkinsAPIClientFromSecret(t *testing.T, jenkins *v1alpha2.Jenkins, jenkinsAPIURL string) (jenkinsclient.Jenkins, error) {
	t.Log("Creating Jenkins API client from secret")

	adminSecret := &corev1.Secret{}
	namespaceName := types.NamespacedName{Namespace: jenkins.Namespace, Name: resources.GetOperatorCredentialsSecretName(jenkins)}
	if err := framework.Global.Client.Get(context.TODO(), namespaceName, adminSecret); err != nil {
		return nil, err
	}

	return jenkinsclient.NewUserAndPasswordAuthorization(
		jenkinsAPIURL,
		string(adminSecret.Data[resources.OperatorCredentialsSecretUserNameKey]),
		string(adminSecret.Data[resources.OperatorCredentialsSecretTokenKey]),
	)
}

func verifyJenkinsAPIConnection(t *testing.T, jenkins *v1alpha2.Jenkins, namespace string) (jenkinsclient.Jenkins, func()) {
	var service corev1.Service
	err := framework.Global.Client.Get(context.TODO(), types.NamespacedName{
		Namespace: jenkins.Namespace,
		Name:      resources.GetJenkinsHTTPServiceName(jenkins),
	}, &service)
	require.NoError(t, err)

	podName := resources.GetJenkinsMasterPodName(jenkins)
	port, cleanUpFunc, waitFunc, portForwardFunc, err := setupPortForwardToPod(t, namespace, podName, int(constants.DefaultHTTPPortInt32))
	if err != nil {
		t.Fatal(err)
	}
	go portForwardFunc()
	waitFunc()

	jenkinsAPIURL := jenkinsclient.JenkinsAPIConnectionSettings{
		Hostname:    "localhost",
		Port:        port,
		UseNodePort: false,
	}.BuildJenkinsAPIUrl(service.Name, service.Namespace, service.Spec.Ports[0].Port, service.Spec.Ports[0].NodePort)

	var client jenkinsclient.Jenkins
	if jenkins.Spec.JenkinsAPISettings.AuthorizationStrategy == v1alpha2.ServiceAccountAuthorizationStrategy {
		client, err = createJenkinsAPIClientFromServiceAccount(t, jenkins, jenkinsAPIURL)
	} else {
		client, err = createJenkinsAPIClientFromSecret(t, jenkins, jenkinsAPIURL)
	}

	if err != nil {
		defer cleanUpFunc()
		t.Fatal(err)
	}

	t.Log("I can establish connection to Jenkins API")
	return client, cleanUpFunc
}

func restartJenkinsMasterPod(t *testing.T, jenkins *v1alpha2.Jenkins) {
	t.Log("Restarting Jenkins master pod")
	jenkinsPod := getJenkinsMasterPod(t, jenkins)
	err := framework.Global.Client.Delete(context.TODO(), jenkinsPod)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Jenkins master pod has been restarted")
}

func getJenkinsService(t *testing.T, jenkins *v1alpha2.Jenkins, serviceKind string) *corev1.Service {
	serviceName := constants.OperatorName + "-" + serviceKind + "-" + jenkins.ObjectMeta.Name
	lo := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"metadata.name": serviceName}).String(),
	}
	serviceList, err := framework.Global.KubeClient.CoreV1().Services(jenkins.Namespace).List(lo)

	require.NoError(t, err)
	require.Equal(t, 1, len(serviceList.Items), fmt.Sprintf("'%s' service not found", serviceName))

	return &serviceList.Items[0]
}
