package main

import (
	"context"
	"fmt"
	"github.com/google/martian/log"
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

const devKubeconfigPath = "kubeconfig"

var nodeDns map[string]string
var mutex sync.Mutex

func init() {
	nodeDns = make(map[string]string)
}
func main() {

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
			if newDepl.GetAnnotations()["kilo.squat.ai/wireguard-ip"] != "" && newDepl.GetAnnotations()["kilo.squat.ai/wireguard-ip"] == newDepl.GetAnnotations()["kilo.squat.ai/wireguard-ip"] {
				return
			}
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
	go syncToConfigmap(kubeclient, stopCh)
	select {
	case <-stopCh:
		log.Infof("收到 kill 信号， 主动退出")
		os.Exit(0)
	}
}

func syncToConfigmap(kubeclient *kubernetes.Clientset, stop <-chan struct{}) {

	ticker := time.NewTicker(10 * time.Second)
	select {
	case <-ticker.C:

		cm, err := kubeclient.CoreV1().ConfigMaps("kube-system").Get(context.Background(), "dns-node", v1.GetOptions{})
		if err != nil {
			// create
			if k8serrors.IsNotFound(err) {
				var configmap corev1.ConfigMap
				configmap.Name = "dns-node"
				configmap.Namespace = "kube-system"
				buildConfigmap(&configmap)
				if _, err := kubeclient.CoreV1().ConfigMaps("kube-system").Create(context.Background(), &configmap, v1.CreateOptions{}); err != nil {
					log.Errorf("fail create configmap: %s", err)
				}
				return
			}
			log.Errorf("fail get configmap: %s", err)
			return
		}
		// update
		buildConfigmap(cm)
		if _, err := kubeclient.CoreV1().ConfigMaps("kube-system").Update(context.Background(), cm, v1.UpdateOptions{}); err != nil {
			log.Errorf("fail update configmap: %s", err)
			return
		}
		log.Debugf("sync to configmap success")
	case <-stop:
		log.Infof("exit syncToConfigmap")
		ticker.Stop()
		return
	}

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
