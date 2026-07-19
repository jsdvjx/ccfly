//go:build !darwin

package mesh

// 非 darwin 平台没有 root helper,/etc/resolver 概念不存在;sniManager.status 的
// darwin 分支在编译期仍引用本符号。linux/windows 的清单权威是进程内 dnsPolicyService。
func managedResolverDomains() []string { return nil }
