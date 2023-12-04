package v1beta1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/ghw/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PciDeviceDriver     = "harvesterhci.io/pcideviceDriver"
	PluginNamePrefix    = "/var/lib/kubelet/device-plugins/kubevirt-"
	SocketFileNameLimit = 108
	VFSuffix            = "VIRTUAL_FUNCTION"
	ShortenedVFSuffix   = "VF"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// PCIDevice is the Schema for the pcidevices API
type PCIDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PCIDeviceSpec   `json:"spec,omitempty"`
	Status PCIDeviceStatus `json:"status,omitempty"`
}

// PCIDeviceStatus defines the observed state of PCIDevice
type PCIDeviceStatus struct {
	Address           string `json:"address"`
	VendorID          string `json:"vendorId"`
	DeviceID          string `json:"deviceId"`
	ClassID           string `json:"classId"`
	IOMMUGroup        string `json:"iommuGroup"`
	NodeName          string `json:"nodeName"`
	ResourceName      string `json:"resourceName"`
	Description       string `json:"description"`
	KernelDriverInUse string `json:"kernelDriverInUse,omitempty"`
}

func description(dev *pci.Device) string {
	var vendorName string
	if dev.Vendor.Name != util.UNKNOWN {
		vendorName = dev.Vendor.Name
	} else {
		vendorName = fmt.Sprintf("Vendor %s", dev.Vendor.ID)
	}
	var deviceName string
	if dev.Product.Name != util.UNKNOWN {
		deviceName = dev.Product.Name
	} else {
		deviceName = fmt.Sprintf("Device %s", dev.Product.ID)
	}
	var className string
	if dev.Subclass.Name != util.UNKNOWN {
		className = dev.Subclass.Name
	} else if dev.Class.Name != util.UNKNOWN {
		className = dev.Class.Name
	} else {
		className = fmt.Sprintf("Class %s%s", dev.Class.ID, dev.Subclass.ID)
	}
	return fmt.Sprintf("%s: %s %s", className, vendorName, deviceName)
}

func strip(s string) string {
	// Make a Regex to say we only want
	reg, err := regexp.Compile("[^a-zA-Z0-9]+")
	if err != nil {
		fmt.Printf("%v", err)
	}
	processedString := reg.ReplaceAllString(s, "")
	return processedString
}

func extractVendorNameFromBrackets(vendorName string) string {
	// Make a Regex to say we only want
	reg, err := regexp.Compile(`\[([^\]]+)\]`)
	if err != nil {
		fmt.Printf("%v", err)
	}
	matches := reg.FindStringSubmatch(vendorName)
	preSlash := strings.Split(matches[1], "/")[0]
	return strip(preSlash)
}

func resourceName(dev *pci.Device) string {
	var vendorBase string
	// if vendor name has a '[name]', then use that
	if strings.Contains(dev.Vendor.Name, "[") {
		vendorBase = extractVendorNameFromBrackets(dev.Vendor.Name)
	} else {
		vendorBase = strip(strings.Split(dev.Vendor.Name, " ")[0])
	}
	vendorCleaned := strings.ToLower(
		strings.ReplaceAll(vendorBase, " ", ""),
	) + ".com"
	if dev.Product.Name != util.UNKNOWN {
		productCleaned := strings.TrimSpace(dev.Product.Name)
		productCleaned = strings.ToUpper(productCleaned)
		productCleaned = strings.Replace(productCleaned, "/", "_", -1)
		productCleaned = strings.Replace(productCleaned, ".", "_", -1)
		reg, _ := regexp.Compile(`\s+`)
		productCleaned = reg.ReplaceAllString(productCleaned, "_") // Replace all spaces with underscore
		reg, _ = regexp.Compile("[^a-zA-Z0-9_.]+")
		productCleaned = reg.ReplaceAllString(productCleaned, "") // Removes any char other than alphanumeric and underscore
		return trimResourceNameIfNeeded(vendorCleaned, productCleaned, dev.Product.ID)
	}
	// If the pcidb doesn't have the deviceId, just show the deviceId
	return fmt.Sprintf("%s/%s", vendorCleaned, dev.Product.ID)
}

func (status *PCIDeviceStatus) Update(dev *pci.Device, hostname string, iommuGroups map[string]int) {
	status.Address = dev.Address
	status.VendorID = dev.Vendor.ID
	status.DeviceID = dev.Product.ID
	status.ClassID = fmt.Sprintf("%s%s", dev.Class.ID, dev.Subclass.ID)
	// Generate the ResourceName field, this is used by KubeVirt to schedule the VM to the node
	status.ResourceName = resourceName(dev)
	status.Description = description(dev)
	group, ok := iommuGroups[dev.Address]
	if ok {
		status.IOMMUGroup = strconv.Itoa(group)
	}
	status.KernelDriverInUse = dev.Driver
	status.NodeName = hostname
}

type PCIDeviceSpec struct {
}

func PCIDeviceNameForHostname(address string, hostname string) string {
	addrDNSsafe := strings.ReplaceAll(strings.ReplaceAll(address, ":", ""), ".", "")
	return fmt.Sprintf(
		"%s-%s",
		hostname,
		addrDNSsafe,
	)
}

func NewPCIDeviceForHostname(dev *pci.Device, hostname string) PCIDevice {
	name := PCIDeviceNameForHostname(dev.Address, hostname)
	pciDevice := PCIDevice{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				PciDeviceDriver: dev.Driver,
			},
		},
		Status: PCIDeviceStatus{
			Address:           dev.Address,
			VendorID:          dev.Vendor.ID,
			DeviceID:          dev.Product.ID,
			ClassID:           fmt.Sprintf("%s%s", dev.Class.ID, dev.Subclass.ID),
			NodeName:          hostname,
			ResourceName:      resourceName(dev),
			Description:       description(dev),
			KernelDriverInUse: dev.Driver,
		},
	}
	return pciDevice
}

// if plugin name is going to exceed 108 chars due to 108 char limit on socket length
// then we need to trim name and switch to using ID https://man7.org/linux/man-pages/man7/unix.7.html
func trimResourceNameIfNeeded(vendorCleaned, productCleaned, ID string) string {
	fullSocketName := fmt.Sprintf("%s/%s-%s.sock", PluginNamePrefix, vendorCleaned, productCleaned)
	if len(fullSocketName) > SocketFileNameLimit {
		if strings.Contains(productCleaned, VFSuffix) {
			// replace VIRTUAL_FUNCTION with VF and retry check
			// to ensure that now new name is lower than socket file limit
			productCleaned = strings.ReplaceAll(productCleaned, VFSuffix, ShortenedVFSuffix)
			return trimResourceNameIfNeeded(vendorCleaned, productCleaned, ID)
		}
		return fmt.Sprintf("%s/%s", vendorCleaned, ID)
	}
	return fmt.Sprintf("%s/%s", vendorCleaned, productCleaned)
}
