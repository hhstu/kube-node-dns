package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"github.com/google/martian/log"
	"io"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/sample-controller/pkg/signals"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	devKubeconfigPath  = "kubeconfig"
	configmapNamespace = "kube-system"
	limitLine          = "####################### no write after this line, 本行之后为自动生成内容，请勿编辑\n"
)

var nodeDns map[string]string
var mutex sync.Mutex

var configmapName string
var hostsFile string

func init() {
	flag.StringVar(&configmapName, "configmap_name", "", "configmap name for write")
	flag.StringVar(&hostsFile, "hosts_file", "", "hosts_file to write, use if configmap_name is null")
	flag.Parse()
	nodeDns = make(map[string]string)
}
func main() {

	if configmapName == "" && hostsFile == "" {
		log.Errorf("configmap_name or hosts_file must have one")
		os.Exit(1)
	}

	kubeconfig, err := clientcmd.BuildConfigFromFlags("", GetKubeConfigFile(""))
	if err != nil {
		log.Errorf("初始化 kubeconfig 失败: ", err)
		os.Exit(1)
	}

	kubeclient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		log.Errorf("初始化 kube-client 失败：", err.Error())
		os.Exit(1)
	}
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeclient, time.Second*30)
	nodeInformer := kubeInformerFactory.Core().V1().Nodes().Informer()
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node := obj.(*corev1.Node)
			cacheToMap(node)
		},
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*corev1.Node)
			oldDepl := old.(*corev1.Node)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			log.Infof("node update %s %s", newDepl.Name, newDepl.GetAnnotations()["kilo.squat.ai/wireguard-ip"])
			cacheToMap(newDepl)
		},
		// todo
		DeleteFunc: nil,
	})
	stopCh := signals.SetupSignalHandler()
	kubeInformerFactory.Start(stopCh)
	if ok := cache.WaitForCacheSync(stopCh, func() bool { return true }); !ok {
		log.Errorf("failed to wait for caches to sync")
	}
	go loop(kubeclient, stopCh)
	select {
	case <-stopCh:
		log.Infof("收到 kill 信号， 主动退出")
		os.Exit(0)
	}
}

func loop(kubeclient *kubernetes.Clientset, stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	for {
		select {
		case <-ticker.C:
			if configmapName != "" {
				syncToConfigmap(kubeclient)
			}
			if hostsFile != "" {
				syncToHostFile()
			}
		case <-stop:
			log.Infof("exit syncToConfigmap")
			ticker.Stop()
			return
		}
	}
}

func syncToHostFile() {

	file, err := os.Open(hostsFile)
	if err != nil {
		log.Errorf("fail open file %s %s", hostsFile, err.Error())
		return
	}
	defer file.Close()
	var newFile string
	br := bufio.NewReader(file)
	for {
		a, _, c := br.ReadLine()
		if c == io.EOF {
			break
		}
		line := string(a) + "\n"
		if line == limitLine {
			break
		}
		newFile += line
	}
	newFile += limitLine

	for node, ip := range nodeDns {
		newFile += fmt.Sprintf("%s %s \n", ip, node)
	}

	if err := os.WriteFile(hostsFile, []byte(newFile), 0644); err != nil {
		log.Errorf("fail write file %s %s", hostsFile, err.Error())
		return
	}
	log.Debugf("sync to host success")
}

func syncToConfigmap(kubeclient *kubernetes.Clientset) {
	cm, err := kubeclient.CoreV1().ConfigMaps(configmapNamespace).Get(context.Background(), configmapName, v1.GetOptions{})
	if err != nil {
		// create
		if k8serrors.IsNotFound(err) {
			var configmap corev1.ConfigMap
			configmap.Name = configmapName
			configmap.Namespace = configmapNamespace
			buildConfigmap(&configmap)
			if _, err := kubeclient.CoreV1().ConfigMaps(configmapNamespace).Create(context.Background(), &configmap, v1.CreateOptions{}); err != nil {
				log.Errorf("fail create configmap: %s", err)
			}
			return
		}
		log.Errorf("fail get configmap: %s", err)
		return
	}
	// update
	buildConfigmap(cm)
	if _, err := kubeclient.CoreV1().ConfigMaps(configmapNamespace).Update(context.Background(), cm, v1.UpdateOptions{}); err != nil {
		log.Errorf("fail update configmap: %s", err)
		return
	}
	log.Debugf("sync to configmap success")
}

func buildConfigmap(cm *corev1.ConfigMap) {
	var dnsnodes strings.Builder
	for node, ip := range nodeDns {
		dnsnodes.WriteString(fmt.Sprintf("%s\t%s \n", ip, node))
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data["dns-nodes"] = dnsnodes.String()
}

func cacheToMap(node *corev1.Node) {
	wireguardIpCidr := node.GetAnnotations()["kilo.squat.ai/wireguard-ip"]
	if wireguardIpCidr != "" {
		peers := strings.Split(wireguardIpCidr, "/")
		mutex.Lock()
		defer mutex.Unlock()
		nodeDns[node.Name] = peers[0]
		return
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			func() {
				mutex.Lock()
				defer mutex.Unlock()
				nodeDns[node.Name] = addr.Address
			}()
			return
		}
	}

}

func GetKubeConfigFile(kubefilePath string) string {
	if kubefilePath != "" {
		return kubefilePath
	}
	if "" == os.Getenv("KUBERNETES_SERVICE_HOST") {
		return devKubeconfigPath
	}
	return ""
}
