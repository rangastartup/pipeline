package helm

import (
	"fmt"
	"github.com/banzaicloud/banzai-types/components"
	"github.com/banzaicloud/banzai-types/components/helm"
	"github.com/banzaicloud/banzai-types/constants"
	"github.com/banzaicloud/banzai-types/utils"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/helm/cmd/helm/installer"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/helm/helmpath"
	"net/http"
	"time"
	"strings"
	"github.com/spf13/viper"
	"os"
	"github.com/pkg/errors"
	"k8s.io/helm/pkg/repo"
	"k8s.io/helm/pkg/getter"
	helm_env "k8s.io/helm/pkg/helm/environment"
	"path/filepath"
	"k8s.io/helm/pkg/downloader"
)

const (
	stableRepository = "stable"
	stableRepositoryURL = "https://kubernetes-charts.storage.googleapis.com"
	banzaiRepository = "banzaicloud-stable"
	banzaiRepositoryURL = "http://kubernetes-charts.banzaicloud.com"
)
var settings helm_env.EnvSettings

//Create ServiceAccount and AccountRoleBinding
func PreInstall(helmInstall *helm.Install) error {

	utils.LogInfo(constants.TagHelmInstall, "start pre-install")

	_, client, err := getKubeClient(helmInstall.KubeContext)
	if err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "could not get kubernetes client: %s", err)
		return err
	}

	v1MetaData := metav1.ObjectMeta{
		Name: helmInstall.ServiceAccount, // "tiller",
	}

	serviceAccount := &apiv1.ServiceAccount{
		ObjectMeta: v1MetaData,
	}
	utils.LogInfo(constants.TagHelmInstall, "create service account")
	_, err = client.CoreV1().ServiceAccounts(helmInstall.Namespace).Create(serviceAccount)
	if err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "create service account failed: %s", err)
		return err
	}

	clusterRoleBinding := &v1.ClusterRoleBinding{
		ObjectMeta: v1MetaData,
		RoleRef: v1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     helmInstall.ServiceAccount, // "tiller",
		},
		Subjects: []v1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      helmInstall.ServiceAccount, // "tiller",
				Namespace: helmInstall.Namespace,
			}},
	}
	utils.LogInfo(constants.TagHelmInstall, "create cluster role bindings")
	_, err = client.RbacV1().ClusterRoleBindings().Create(clusterRoleBinding)
	if err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "create role bindings failed: %s", err)
		return err
	}
	clusterRole := &v1.ClusterRole{
		ObjectMeta: v1MetaData,
		Rules: []v1.PolicyRule{{
			APIGroups: []string{
				"*",
			},
			Resources: []string{
				"*",
			},
			Verbs: []string{
				"*",
			},
		},
		{
			NonResourceURLs: []string{
				"*",
			},
			Verbs: []string{
				"*",
			},
		}},
	}
	utils.LogInfo(constants.TagHelmInstall, "create cluster roles")
	_, err = client.RbacV1().ClusterRoles().Create(clusterRole)
	if err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "create roles failed: %s", err)
		return err
	}

	return nil
}

// RetryHelmInstall retries for a configurable time/interval
// Azure AKS sometimes failing because of TLS handshake timeout, there are several issues on GitHub about that:
// https://github.com/Azure/AKS/issues/112, https://github.com/Azure/AKS/issues/116, https://github.com/Azure/AKS/issues/14
func RetryHelmInstall(helmInstall *helm.Install, clusterType string) error {
	retryAttempts := viper.GetInt("dev.retryAttempt")
	retrySleepSeconds := viper.GetInt("dev.retrySleepSeconds")

	logTag := "RetryHelmInstall"
	for i := 0; i <= retryAttempts; i++ {
		utils.LogDebugf(logTag, "Waiting %d/%d", i, retryAttempts)
		response := Install(helmInstall)
		if strings.Contains(response.Message, "net/http: TLS handshake timeout") {
			time.Sleep(time.Duration(retrySleepSeconds) * time.Second)
			continue
		}
		return nil
	}
	switch clusterType {
	case constants.Amazon:
		utils.LogError(logTag, "Timeout during waiting for AWS Control Plane to become healthy..")
	case constants.Azure:
		utils.LogError(logTag, "Timeout during waiting for AKS to become healthy..")
		utils.LogError(logTag, "https://github.com/Azure/AKS/issues/116")
	}
	return fmt.Errorf("timeout during helm install")
}

func initializeEnvSettings(clusterName string) {
	settings.Home = helmpath.Home(stateStorePath + clusterName + helmPostFix)
}

func downloadChartFromRepo(name string) (string, error) {
	dl := downloader.ChartDownloader{
		HelmHome: settings.Home,
		Getters:  getter.All(settings),
	}
	if _, err := os.Stat(settings.Home.Archive()); os.IsNotExist(err) {
		os.MkdirAll(settings.Home.Archive(), 0744)
	}
	filename, _, err := dl.DownloadTo(name, "", settings.Home.Archive())
	if err == nil {
		lname, err := filepath.Abs(filename)
		if err != nil {
			return filename, errors.Wrapf(err, "Could not create absolute path from %s", filename)
		}
		utils.LogDebugf("downloadChartFromRepo", "Fetched %s to %s", name, filename)
		return lname, nil
	}

	return filename, errors.Errorf("Failed to download %q", name)
}

// Installs helm client on the cluster
func installHelmClient(helmInstall *helm.Install) error {
	const logTag  = "installHelmClient"
	settings.Home = helmInstall.HomePath
	if err := ensureDirectories(helmInstall.HomePath); err != nil {
		return errors.Wrap(err, "Initializing helm directories failed!")
	}

	if err := ensureDefaultRepos(helmInstall.HomePath); err != nil {
		return errors.Wrap(err, "Setting up default repos failed!")
	}

	utils.LogInfo(logTag, "Initializing helm client succeeded, happy helming!")
	return nil
}

func ensureDirectories(home helmpath.Home) error {
	const logTag = "ensureDirectories"
	configDirectories := []string{
		home.String(),
		home.Repository(),
		home.Cache(),
		home.LocalRepository(),
		home.Plugins(),
		home.Starters(),
		home.Archive(),
	}
	for _, p := range configDirectories {
		if fi, err := os.Stat(p); err != nil {
			utils.LogInfof( logTag,"Creating %s", p)
			if err := os.MkdirAll(p, 0755); err != nil {
				return errors.Wrapf(err,"Could not create %s", p)
			}
		} else if !fi.IsDir() {
			return errors.Errorf("%s must be a directory", p)
		}
	}
	return nil
}

func ensureDefaultRepos(home helmpath.Home) error {
	const logTag = "ensureDefault Repos"
	repoFile := home.RepositoryFile()
	if fi, err := os.Stat(repoFile); err != nil {
		utils.LogInfof(logTag, "Creating %s \n", repoFile)
		f := repo.NewRepoFile()
		sr, err := initRepo(stableRepository, stableRepositoryURL ,home.CacheIndex(stableRepository))
		if err != nil {
			return errors.Wrapf(err, "Cannot init stable repo")
		}
		br, err := initRepo(banzaiRepository, banzaiRepositoryURL, home.CacheIndex(banzaiRepository))
		if err != nil {
			return errors.Wrapf(err, "Cannot init banzai repo")
		}
		f.Add(sr, br)
		if err := f.WriteFile(repoFile, 0644); err != nil {
			return errors.Wrap(err, "Cannot create file")
		}
	} else if fi.IsDir() {
		return errors.Errorf("%s must be a file, not a directory", repoFile)
	}
	return nil
}

func initRepo(repoName string, repoUrl string, cacheFile string) (*repo.Entry, error) {
	const logTag = "initStableRepo"
	utils.LogInfof(logTag, "Adding %s repo with URL: %s \n", repoName, repoUrl)
	c := repo.Entry{
		Name:  repoName,
		URL:   repoUrl,
		Cache: cacheFile,
	}
	r, err := repo.NewChartRepository(&c, getter.All(settings))
	if err != nil {
		return nil, errors.Wrap(err, "Cannot create a new ChartRepo")
	}

	// In this case, the cacheFile is always absolute. So passing empty string
	// is safe.
	if err := r.DownloadIndexFile(""); err != nil {
		return nil, errors.Errorf("Looks like %q is not a valid chart repository or cannot be reached: %s", repoUrl, err.Error())
	}

	return &c, nil
}

// Install uses Kubernetes client to install Tiller.
func Install(helmInstall *helm.Install) *components.BanzaiResponse {

	//Installing helm client
	utils.LogInfo(constants.TagHelmInstall, "Installing helm client!")
	if err := installHelmClient(helmInstall); err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "%+v\n", err)
		return &components.BanzaiResponse{
			StatusCode: http.StatusInternalServerError,
			Message:    err.Error(),
		}
	}
	utils.LogInfo(constants.TagHelmInstall, "Helm client install succeeded")

	err := PreInstall(helmInstall)
	if err != nil {
		return &components.BanzaiResponse{
			StatusCode: http.StatusInternalServerError,
			Message:    err.Error(),
		}
	}

	opts := installer.Options{
		Namespace:      helmInstall.Namespace,
		ServiceAccount: helmInstall.ServiceAccount,
		UseCanary:      helmInstall.Canary,
		ImageSpec:      helmInstall.ImageSpec,
		MaxHistory:     helmInstall.MaxHistory,
	}
	_, kubeClient, err := getKubeClient(helmInstall.KubeContext)
	if err != nil {
		utils.LogErrorf(constants.TagHelmInstall, "could not get kubernetes client: %s", err)
		return &components.BanzaiResponse{
			StatusCode: http.StatusBadRequest,
			Message:    fmt.Sprintf("could not get kubernetes client: %s", err),
		}
	}
	if err := installer.Install(kubeClient, &opts); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			utils.LogErrorf(constants.TagHelmInstall, "error installing: %s", err)
			return &components.BanzaiResponse{
				StatusCode: http.StatusInternalServerError,
				Message:    fmt.Sprintf("error installing: %s", err),
			}
		}
		if helmInstall.Upgrade {
			if err := installer.Upgrade(kubeClient, &opts); err != nil {
				utils.LogErrorf(constants.TagHelmInstall, "error when upgrading: %s", err)
				return &components.BanzaiResponse{
					StatusCode: http.StatusInternalServerError,
					Message:    fmt.Sprintf("error when upgrading: %s", err),
				}
			}
			utils.LogInfo(constants.TagHelmInstall, "Tiller (the Helm server-side component) has been upgraded to the current version.")
		} else {
			utils.LogInfo(constants.TagHelmInstall, "Warning: Tiller is already installed in the cluster.")
		}
	} else {
		utils.LogInfo(constants.TagHelmInstall, "Tiller (the Helm server-side component) has been installed into your Kubernetes Cluster.")
	}
	utils.LogInfo(constants.TagHelmInstall, "Helm install finished")
	return &components.BanzaiResponse{
		StatusCode: http.StatusOK,
	}
}

// getKubeClient creates a Kubernetes config and client for a given kubeconfig context.
func getKubeClient(context string) (*rest.Config, kubernetes.Interface, error) {
	config, err := configForContext(context)
	if err != nil {
		return nil, nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get Kubernetes client: %s", err)
	}
	return config, client, nil
}

// configForContext creates a Kubernetes REST client configuration for a given kubeconfig context.
func configForContext(context string) (*rest.Config, error) {
	config, err := kube.GetConfig(context).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get Kubernetes config for context %q: %s", context, err)
	}
	return config, nil
}