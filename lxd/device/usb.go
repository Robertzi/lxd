package device

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/lxc/lxd/lxd/device/config"
	"github.com/lxc/lxd/lxd/instance"
	"github.com/lxc/lxd/shared"
)

type usb struct {
	deviceCommon
}

// validateConfig checks the supplied config for correctness.
func (d *usb) validateConfig() error {
	if d.instance.Type() != instance.TypeContainer {
		return ErrUnsupportedDevType
	}

	rules := map[string]func(string) error{
		"vendorid":  shared.IsDeviceID,
		"productid": shared.IsDeviceID,
		"uid":       shared.IsUnixUserID,
		"gid":       shared.IsUnixUserID,
		"mode":      shared.IsOctalFileMode,
		"required":  shared.IsBool,
	}

	err := config.ValidateDevice(rules, d.config)
	if err != nil {
		return err
	}

	return nil
}

// validateEnvironment checks the runtime environment for correctness.
func (d *usb) validateEnvironment() error {
	return nil
}

// Register is run after the device is started or when LXD starts.
func (d *usb) Register() error {
	// Extract variables needed to run the event hook so that the reference to this device
	// struct is not needed to be kept in memory.
	devicesPath := d.instance.DevicesPath()
	deviceConfig := d.config
	deviceName := d.name
	state := d.state

	// Handler for when a USB event occurs.
	f := func(usb USBDevice) (*RunConfig, error) {
		if !USBIsOurDevice(deviceConfig, &usb) {
			return nil, nil
		}

		runConf := RunConfig{}

		if usb.Action == "add" {
			err := unixDeviceSetupCharNum(state, devicesPath, "unix", deviceName, deviceConfig, usb.Major, usb.Minor, usb.Path, false, &runConf)
			if err != nil {
				return nil, err
			}
		} else if usb.Action == "remove" {
			relativeTargetPath := strings.TrimPrefix(usb.Path, "/")
			err := unixDeviceRemove(devicesPath, "unix", deviceName, relativeTargetPath, &runConf)
			if err != nil {
				return nil, err
			}

			// Add a post hook function to remove the specific USB device file after unmount.
			runConf.PostHooks = []func() error{func() error {
				err := unixDeviceDeleteFiles(state, devicesPath, "unix", deviceName, relativeTargetPath)
				if err != nil {
					return fmt.Errorf("Failed to delete files for device '%s': %v", deviceName, err)
				}

				return nil
			}}
		}

		runConf.Uevents = append(runConf.Uevents, usb.UeventParts)

		return &runConf, nil
	}

	USBRegisterHandler(d.instance, d.name, f)

	return nil
}

// Start is run when the device is added to the instance.
func (d *usb) Start() (*RunConfig, error) {
	err := d.validateEnvironment()
	if err != nil {
		return nil, err
	}

	usbs, err := d.loadUsb()
	if err != nil {
		return nil, err
	}

	runConf := RunConfig{}

	for _, usb := range usbs {
		if !USBIsOurDevice(d.config, &usb) {
			continue
		}

		err := unixDeviceSetupCharNum(d.state, d.instance.DevicesPath(), "unix", d.name, d.config, usb.Major, usb.Minor, usb.Path, false, &runConf)
		if err != nil {
			return nil, err
		}
	}

	if shared.IsTrue(d.config["required"]) && len(runConf.Mounts) <= 0 {
		return nil, fmt.Errorf("Required USB device not found")
	}

	return &runConf, nil
}

// Stop is run when the device is removed from the instance.
func (d *usb) Stop() (*RunConfig, error) {
	// Unregister any USB event handlers for this device.
	USBUnregisterHandler(d.instance, d.name)

	runConf := RunConfig{
		PostHooks: []func() error{d.postStop},
	}

	err := unixDeviceRemove(d.instance.DevicesPath(), "unix", d.name, "", &runConf)
	if err != nil {
		return nil, err
	}

	return &runConf, nil
}

// postStop is run after the device is removed from the instance.
func (d *usb) postStop() error {
	// Remove host files for this device.
	err := unixDeviceDeleteFiles(d.state, d.instance.DevicesPath(), "unix", d.name, "")
	if err != nil {
		return fmt.Errorf("Failed to delete files for device '%s': %v", d.name, err)
	}

	return nil
}

// loadUsb scans the host machine for USB devices.
func (d *usb) loadUsb() ([]USBDevice, error) {
	result := []USBDevice{}

	ents, err := ioutil.ReadDir(usbDevPath)
	if err != nil {
		/* if there are no USB devices, let's render an empty list,
		 * i.e. no usb devices */
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	for _, ent := range ents {
		values, err := d.loadRawValues(path.Join(usbDevPath, ent.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return []USBDevice{}, err
		}

		parts := strings.Split(values["dev"], ":")
		if len(parts) != 2 {
			return []USBDevice{}, fmt.Errorf("invalid device value %s", values["dev"])
		}

		usb, err := USBDeviceLoad(
			"add",
			values["idVendor"],
			values["idProduct"],
			parts[0],
			parts[1],
			values["busnum"],
			values["devnum"],
			values["devname"],
			[]string{},
			0,
		)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		result = append(result, usb)
	}

	return result, nil
}

func (d *usb) loadRawValues(p string) (map[string]string, error) {
	values := map[string]string{
		"idVendor":  "",
		"idProduct": "",
		"dev":       "",
		"busnum":    "",
		"devnum":    "",
	}

	for k := range values {
		v, err := ioutil.ReadFile(path.Join(p, k))
		if err != nil {
			return nil, err
		}

		values[k] = strings.TrimSpace(string(v))
	}

	return values, nil
}
