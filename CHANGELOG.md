# Changelog

## 0.1.0 (2026-11-22)

- feat: 新建 runtime-python 插件骨架，复用 kore.plugin.v1 握手与会话循环
- feat: runtime-python.versions / runtime-python.venv.create 方法级参数校验与结构化结果，版本白名单 3.9-3.13，路径限制在配置根目录内，不真建 venv
- feat: 基础故障注入测试（kill -9 / 超时），验证方法级错误返回
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner + gzip -9n）
