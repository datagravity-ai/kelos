package integration

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("AgentConfig skill file validation", func() {
	const ns = "test-agentconfig-validation"

	BeforeEach(func() {
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		if err != nil {
			Expect(apierrors.IsAlreadyExists(err)).To(BeTrue())
		}
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &kelos.AgentConfig{}, client.InNamespace(ns))).To(Succeed())
	})

	configWithFiles := func(name string, files []kelos.SkillFile) *kelos.AgentConfig {
		return &kelos.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.AgentConfigSpec{Plugins: []kelos.PluginSpec{{Name: "tools", Skills: []kelos.SkillDefinition{{
				Name: "review", Content: "# Review", Files: files,
			}}}}},
		}
	}

	It("stores supporting files, including executable scripts and hidden paths", func() {
		files := []kelos.SkillFile{
			{Path: "references/nested/check.md", Content: "Checklist\n"},
			{Path: "scripts/check.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
			{Path: ".hidden", Content: "Metadata"},
			{Path: "..notes", Content: "Notes"},
		}
		ac := configWithFiles("bundle", files)
		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		got := &kelos.AgentConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ac.Name, Namespace: ns}, got)).To(Succeed())
		Expect(got.Spec.Plugins).To(HaveLen(1))
		Expect(got.Spec.Plugins[0].Skills).To(HaveLen(1))
		Expect(got.Spec.Plugins[0].Skills[0].Files).To(Equal(files))
	})

	It("accepts a v1alpha2 bundle larger than the annotation size limit", func() {
		ac := configWithFiles("large-bundle", []kelos.SkillFile{{Path: "reference.md", Content: strings.Repeat("x", 300*1024)}})
		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		got := &kelos.AgentConfig{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ac.Name, Namespace: ns}, got)).To(Succeed())
		Expect(got.Spec.Plugins[0].Skills[0].Files).To(Equal(ac.Spec.Plugins[0].Skills[0].Files))
	})

	It("rejects invalid paths on create and update", func() {
		ac := configWithFiles("update-path", []kelos.SkillFile{{Path: "valid.md", Content: "Reference"}})
		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		for i, path := range []string{"", ".", "..", "../outside", "/absolute", "references/../file", "references/./file", "references//file", "references/", "references\\file", strings.Repeat("x", 4097)} {
			files := []kelos.SkillFile{{Path: path, Content: "Reference"}}
			err := k8sClient.Create(ctx, configWithFiles(fmt.Sprintf("invalid-path-%d", i), files))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "create path %q: %v", path, err)
			ac.Spec.Plugins[0].Skills[0].Files = files
			err = k8sClient.Update(ctx, ac)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "update path %q: %v", path, err)
		}
	})

	It("rejects duplicate paths on create and update", func() {
		files := []kelos.SkillFile{{Path: "reference.md", Content: "First"}, {Path: "reference.md", Content: "Second"}}
		err := k8sClient.Create(ctx, configWithFiles("duplicate-paths", files))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "create duplicate paths: %v", err)
		ac := configWithFiles("update-duplicates", files[:1])
		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		ac.Spec.Plugins[0].Skills[0].Files = files
		err = k8sClient.Update(ctx, ac)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "update duplicate paths: %v", err)
	})
})
