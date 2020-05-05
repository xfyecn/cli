package databases

import (
	"errors"
	"fmt"
	"log"
	"os"

	apiv1alpha1 "kubedb.dev/apimachinery/apis/kubedb/v1alpha1"
	cs "kubedb.dev/apimachinery/client/clientset/versioned"

	shell "github.com/codeskyblue/go-sh"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

func addElasticsearchCMD(cmds *cobra.Command) {
	var esName string
	var namespace string
	var esCmd = &cobra.Command{
		Use:   "elasticsearch",
		Short: "Use to operate elasticsearch pods",
		Long:  `Use this cmd to operate elasticsearch pods.`,
		Run: func(cmd *cobra.Command, args []string) {
			println("Use subcommands such as connect or apply to operate PSQL pods")
		},
	}
	var esConnectCmd = &cobra.Command{
		Use:   "connect",
		Short: "Connect to a elasticsearch object's pod",
		Long:  `Use this cmd to exec into a elasticsearch object's primary pod.`,
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				log.Fatal("Enter elasticsearch object's name as an argument")
			}
			esName = args[0]

			podName, secretName, err := getElasticsearchInfo(namespace, esName)
			if err != nil {
				log.Fatal(err)
			}

			auth, tunnel, err := tunnelToDBPod(esPort, namespace, podName, secretName)
			if err != nil {
				log.Fatal("Couldn't tunnel through. Error = ", err)
			}
			esConnect(auth, tunnel.Local)
			tunnel.Close()
		},
	}

	cmds.AddCommand(esCmd)
	esCmd.AddCommand(esConnectCmd)
	esCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Namespace of the elasticsearch object to connect to.")
}

func esConnect(auth *corev1.Secret, localPort int) {
	sh := shell.NewSession()
	err := sh.Command("docker", "run", "--network=host", "-it",
		"-e", fmt.Sprintf("USERNAME=%s", auth.Data[esAdminUsername]), "-e", fmt.Sprintf("PASSWORD=%s", auth.Data[esAdminPassword]),
		"-e", fmt.Sprintf("ADDRESS=localhost:%d", localPort),
		alpineCurlImg,
	).SetStdin(os.Stdin).Run()
	if err != nil {
		log.Fatalln(err)
	}
}

func getElasticsearchInfo(namespace, dbObjectName string) (podName string, secretName string, err error) {
	config, err := getKubeConfig()
	if err != nil {
		log.Fatalf("Could not get Kubernetes config: %s", err)
	}
	dbClient := cs.NewForConfigOrDie(config)
	elasticsearch, err := dbClient.KubedbV1alpha1().Elasticsearches(namespace).Get(dbObjectName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	if elasticsearch.Status.Phase != apiv1alpha1.DatabasePhaseRunning {
		return "", "", errors.New("elasticsearch is not ready")
	}
	client := kubernetes.NewForConfigOrDie(config)
	secretName = dbObjectName + "-auth"
	_, err = client.CoreV1().Secrets(namespace).Get(secretName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	label := labels.Set{
		apiv1alpha1.LabelDatabaseKind: apiv1alpha1.ResourceKindElasticsearch,
		apiv1alpha1.LabelDatabaseName: dbObjectName,
	}.String()
	pods, err := client.CoreV1().Pods(namespace).List(metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return "", "", err
	}
	for _, pod := range pods.Items {
		if elasticsearch.Spec.Topology == nil || pod.Labels[esNodeRoleClient] == "set" {
			podName = pod.Name
			break
		}
	}
	return podName, secretName, nil
}
