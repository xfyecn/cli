package databases

import (
	"errors"
	"log"
	"os"
	"strconv"

	apiv1alpha1 "kubedb.dev/apimachinery/apis/kubedb/v1alpha1"
	cs "kubedb.dev/apimachinery/client/clientset/versioned"

	shell "github.com/codeskyblue/go-sh"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"kmodules.xyz/client-go/tools/portforward"
)

func addMemcachedCMD(cmds *cobra.Command) {
	var mcName string
	var namespace string
	var mcCmd = &cobra.Command{
		Use:   "memcached",
		Short: "Use to operate memcached pods",
		Long:  `Use this cmd to operate memcached pods.`,
		Run: func(cmd *cobra.Command, args []string) {
			println("Use subcommands such as connect or apply to operate PSQL pods")
		},
	}
	var mcConnectCmd = &cobra.Command{
		Use:   "connect",
		Short: "Connect to a memcached object's pod",
		Long:  `Use this cmd to exec into a memcached object's primary pod.`,
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				log.Fatal("Enter memcached object's name as an argument")
			}
			mcName = args[0]

			podName, err := getMemcachedInfo(namespace, mcName)
			if err != nil {
				log.Fatal(err)
			}

			tunnel, err := tunnelToMcPod(mcPort, namespace, podName)
			if err != nil {
				log.Fatal("Couldn't tunnel through. Error = ", err)
			}
			mcConnect(tunnel.Local)
			tunnel.Close()
		},
	}

	cmds.AddCommand(mcCmd)
	mcCmd.AddCommand(mcConnectCmd)
	mcCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Namespace of the memcached object to connect to.")
}

func mcConnect(port int) {
	println("Connected to memcached :")
	sh := shell.NewSession()
	err := sh.Command("docker", "run", "--network=host", "-it",
		alpineTelnetImg, "127.0.0.1", strconv.Itoa(port),
	).SetStdin(os.Stdin).Run()
	if err != nil {
		log.Fatalln(err)
	}
}

func getMemcachedInfo(namespace, dbObjectName string) (podName string, err error) {
	config, err := getKubeConfig()
	if err != nil {
		log.Fatalf("Could not get Kubernetes config: %s", err)
	}
	dbClient := cs.NewForConfigOrDie(config)
	memcached, err := dbClient.KubedbV1alpha1().Memcacheds(namespace).Get(dbObjectName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if memcached.Status.Phase != apiv1alpha1.DatabasePhaseRunning {
		return "", errors.New("memcached is not ready")
	}
	//if cluster is enabled
	client := kubernetes.NewForConfigOrDie(config)
	label := labels.Set{
		apiv1alpha1.LabelDatabaseKind: apiv1alpha1.ResourceKindMemcached,
		apiv1alpha1.LabelDatabaseName: dbObjectName,
	}.String()
	pods, err := client.CoreV1().Pods(namespace).List(metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		podName = pod.Name
		break
	}
	return podName, nil
}

func tunnelToMcPod(dbPort int, namespace, podName string) (*portforward.Tunnel, error) {
	//TODO: Always close the tunnel after using thing function
	config, err := getKubeConfig()
	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	if namespace == "" {
		log.Println("Using default namespace. Enter your namespace using -n=<your-namespace>")
	}

	tunnel := portforward.NewTunnel(client.CoreV1().RESTClient(), config, namespace, podName, dbPort)
	err = tunnel.ForwardPort()
	if err != nil {
		log.Fatalln(err)
	}

	return tunnel, err
}
