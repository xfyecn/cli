package databases

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	apiv1alpha1 "kubedb.dev/apimachinery/apis/kubedb/v1alpha1"
	cs "kubedb.dev/apimachinery/client/clientset/versioned"

	shell "github.com/codeskyblue/go-sh"
	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/bson"
	mgo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"kmodules.xyz/client-go/tools/portforward"
)

func addMongoCMD(cmds *cobra.Command) {
	var mgName string
	var namespace string
	var fileName string
	var command string

	var mgCmd = &cobra.Command{
		Use:   "mongodb",
		Short: "Use to operate mongodb pods",
		Long:  `Use this cmd to operate mongodb pods.`,
		Run: func(cmd *cobra.Command, args []string) {
			println("Use subcommands such as connect or apply to operate PSQL pods")
		},
	}
	var mgConnectCmd = &cobra.Command{
		Use:   "connect",
		Short: "Connect to a mongodb object's pod",
		Long:  `Use this cmd to exec into a mongodb object's primary pod.`,
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) == 0 {
				log.Fatal("Enter mongodb object's name as an argument")
			}
			mgName = args[0]
			//get db pod and secret
			podName, secretName, err := getMongoDBInfo(namespace, mgName)
			if err != nil {
				log.Fatal(err)
			}
			auth, tunnel, err := tunnelToDBPod(mgPort, namespace, podName, secretName)
			if err != nil {
				log.Fatal("Couldn't tunnel through. Error = ", err)
			}
			mgConnect(auth, tunnel.Local)
			tunnel.Close()
		},
	}

	var mgApplyCmd = &cobra.Command{
		Use:   "apply",
		Short: "Apply SQL commands to a mongodb object's pod",
		Long: `Use this cmd to apply mongodb commands from a file to a mongodb object's primary pod.
				Syntax: $ kubedb mongodb apply <mongodb-object-name> -n <namespace> -f <fileName>
				`,
		Run: func(cmd *cobra.Command, args []string) {
			println("Applying SQl")
			if len(args) == 0 {
				log.Fatal("Enter mongodb object's name as an argument. Your commands will be applied on a database inside it's primary pod")
			}
			mgName = args[0]

			if fileName == "" && command == "" {
				log.Fatal(" Use --file or --command to apply supported commands to a mongodb object's pods")
			}

			podName, secretName, err := getMongoDBInfo(namespace, mgName)
			if err != nil {
				log.Fatal(err)
			}
			auth, tunnel, err := tunnelToDBPod(mgPort, namespace, podName, secretName)
			if err != nil {
				log.Fatal("Couldn't tunnel through. Error = ", err)
			}

			if command != "" {
				mgApplyCommand(auth, tunnel.Local, command)
			}

			if fileName != "" {
				mgApplyFile(auth, tunnel.Local, fileName)
			}

			tunnel.Close()
		},
	}

	cmds.AddCommand(mgCmd)
	mgCmd.AddCommand(mgConnectCmd)
	mgCmd.AddCommand(mgApplyCmd)
	mgCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Namespace of the mongodb object to connect to.")

	mgApplyCmd.Flags().StringVarP(&fileName, "file", "f", "", "path to command file")
	mgApplyCmd.Flags().StringVarP(&command, "command", "c", "", "command to execute")
}

func mgConnect(auth *corev1.Secret, localPort int) {
	sh := shell.NewSession()
	sh.ShowCMD = false

	err := sh.Command("docker", "run", "--network=host", "-it",
		"mongo:latest",
		"mongo", "admin",
		"--host=127.0.0.1", fmt.Sprintf("--port=%d", localPort),
		fmt.Sprintf("--username=%s", auth.Data["username"]),
		fmt.Sprintf("--password=%s", auth.Data["password"]),
	).SetStdin(os.Stdin).Run()
	if err != nil {
		log.Fatalln(err)
	}
}

func mgApplyCommand(auth *corev1.Secret, localPort int, command string) {
	sh := shell.NewSession()
	sh.ShowCMD = false
	err := sh.Command("docker", "run", "--network=host", "mongo",
		"mongo", "admin",
		"--host=127.0.0.1", fmt.Sprintf("--port=%d", localPort),
		fmt.Sprintf("--username=%s", auth.Data["username"]),
		fmt.Sprintf("--password=%s", auth.Data["password"]), "--eval",
		fmt.Sprintf("%s", command),
	).SetStdin(os.Stdin).Run()
	if err != nil {
		log.Fatalln("go-sh err = ", err)
	}
	println("Command(s) applied")
}

func mgApplyFile(auth *corev1.Secret, localPort int, fileName string) {
	sh := shell.NewSession()
	sh.ShowCMD = false
	fileName, err := filepath.Abs(fileName)
	if err != nil {
		log.Fatalln(err)
	}
	tempFileName := "/home/mongo.js"

	err = sh.Command("docker", "run", "--network=host",
		"-v", fmt.Sprintf("%s:%s", fileName, tempFileName), "mongo:3.6",
		"mongo", "admin",
		"--host=127.0.0.1", fmt.Sprintf("--port=%d", localPort),
		fmt.Sprintf("--username=%s", auth.Data["username"]),
		fmt.Sprintf("--password=%s", auth.Data["password"]),
		tempFileName,
	).SetStdin(os.Stdin).Run()

	if err != nil {
		log.Fatalln("go-sh err = ", err)
	}
	println("File applied")
}

func getMongoDBInfo(namespace string, dbObjectName string) (podName string, secretName string, err error) {
	config, err := getKubeConfig()
	if err != nil {
		return "", "", err
	}
	dbClient := cs.NewForConfigOrDie(config)
	mongo, err := dbClient.KubedbV1alpha1().MongoDBs(namespace).Get(dbObjectName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	if mongo.Status.Phase != apiv1alpha1.DatabasePhaseRunning {
		return "", "", errors.New("MongoDB is not ready")
	}
	secretName = mongo.Spec.DatabaseSecret.SecretName
	podName = getPrimaryPodName(config, mongo)

	return podName, secretName, nil
}

func getPrimaryPodName(config *rest.Config, mongo *apiv1alpha1.MongoDB) (podName string) {
	if mongo.Spec.ReplicaSet == nil && mongo.Spec.ShardTopology == nil {
		//one mongo, without shard
		podName = fmt.Sprintf("%v-0", mongo.Name)
	}

	if mongo.Spec.ShardTopology != nil {
		//shard, no master
		podName = GetMongosPodName(config, mongo)
	}
	//More than a replica, no shard
	podName, _ = GetReplicaMasterNode(mongo)
	return podName
}

func GetMongosPodName(config *rest.Config, mongo *apiv1alpha1.MongoDB) (mongosPodName string) {
	client := kubernetes.NewForConfigOrDie(config)
	pods, err := client.CoreV1().Pods(mongo.Namespace).List(metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Name, fmt.Sprintf("%v-mongos", mongo.Name)) {
			mongosPodName = pod.Name
		}
	}
	return mongosPodName
}

func GetReplicaMasterNode(mongo *apiv1alpha1.MongoDB) (string, error) {
	replicaNumber := mongo.Spec.Replicas
	if replicaNumber == nil {
		return "", fmt.Errorf("replica is zero")
	}

	fn := func(clientPodName string) (bool, error) {
		client, tunnel, err := ConnectAndPing(mongo, clientPodName, true)
		if err != nil {
			return false, err
		}
		defer tunnel.Close()

		res := make(map[string]interface{})
		if err := client.Database("admin").RunCommand(context.Background(), bson.D{{"isMaster", "1"}}).Decode(&res); err != nil {
			return false, err
		}

		if val, ok := res["ismaster"]; ok && val == true {
			return true, nil
		}
		return false, fmt.Errorf("%v not master node", clientPodName)
	}

	// For MongoDB ReplicaSet, Find out the primary instance.
	// Extract information `IsMaster: true` from the component's status.
	for i := int32(0); i <= *replicaNumber; i++ {
		clientPodName := fmt.Sprintf("%v-%d", mongo.Name, i)
		var isMaster bool
		isMaster, err := fn(clientPodName)
		if err == nil && isMaster {
			return clientPodName, nil
		}
	}
	return "", fmt.Errorf("no primary node")
}

func ConnectAndPing(mongo *apiv1alpha1.MongoDB, clientPodName string, isReplSet bool) (*mgo.Client, *portforward.Tunnel, error) {
	config, err := getKubeConfig()
	if err != nil {
		log.Fatalf("Could not get Kubernetes config: %s", err)
	}
	client := kubernetes.NewForConfigOrDie(config)

	forwardPort := func() (*portforward.Tunnel, error) {

		tunnel := portforward.NewTunnel(
			client.CoreV1().RESTClient(),
			config,
			mongo.Namespace,
			clientPodName,
			27017,
		)

		if err := tunnel.ForwardPort(); err != nil {
			return nil, err
		}
		return tunnel, nil
	}

	tunnel, err := forwardPort()
	if err != nil {
		return nil, nil, err
	}

	user := "root"
	secret, err := client.CoreV1().Secrets(mongo.Namespace).Get(mongo.Spec.DatabaseSecret.SecretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	password := string(secret.Data[mgPassword])

	clientOpts := options.Client().ApplyURI(fmt.Sprintf("mongodb://%s:%s@127.0.0.1:%v", user, password, tunnel.Local))
	if isReplSet {
		clientOpts.SetDirect(true)
	}

	clnt, err := mgo.Connect(context.Background(), clientOpts)
	if err != nil {
		return nil, nil, err
	}

	err = clnt.Ping(context.TODO(), nil)
	if err != nil {
		return nil, nil, err
	}
	return clnt, tunnel, err
}
