package controller

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func requireControllerOwner(object metav1.Object, owner metav1.Object) error {
	controller := metav1.GetControllerOf(object)
	if owner.GetUID() == "" || controller == nil || controller.UID != owner.GetUID() {
		return fmt.Errorf("resource %s/%s is not controlled by expected owner UID", object.GetNamespace(), object.GetName())
	}
	return nil
}
