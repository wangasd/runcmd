#!/bin/bash
set -e

PKG_NAME="runcmd"
VERSION="1.0.0-0001"
BINARY="../runcmd_linux"
WOL="../wol"
DIST_DIR="./dist"
BUILD_DIR="./.build"

echo "==> Building ${PKG_NAME}-${VERSION}.spk ..."

if [ ! -f "${BINARY}" ]; then
    echo "ERROR: ${BINARY} 不存在，请先编译："
    echo "  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o runcmd_linux .."
    exit 1
fi

if [ ! -f "${WOL}" ]; then
    echo "ERROR: ${WOL} 不存在，请将 wol 二进制放到项目根目录"
    exit 1
fi

rm -rf "${BUILD_DIR}"
mkdir -p \
    "${BUILD_DIR}/pkg/ui/images" \
    "${BUILD_DIR}/pkg/nginx" \
    "${BUILD_DIR}/scripts" \
    "${BUILD_DIR}/conf" \
    "${DIST_DIR}"

# ── package.tgz（解压到 /volume1/@appstore/runcmd/）──────────────────
# 二进制
cp "${BINARY}" "${BUILD_DIR}/pkg/runcmd_linux"
chmod +x "${BUILD_DIR}/pkg/runcmd_linux"
cp "${WOL}" "${BUILD_DIR}/pkg/wol"
chmod +x "${BUILD_DIR}/pkg/wol"
# ui/ 放进 package 内（dsmuidir 指向此处）
sed 's/\r//' ui/config > "${BUILD_DIR}/pkg/ui/config"
cp ui/images/* "${BUILD_DIR}/pkg/ui/images/"
# nginx 反代配置（postinst 会 cp 到 /etc/nginx/conf.d/）
cp nginx/dsm.runcmd.conf "${BUILD_DIR}/pkg/nginx/dsm.runcmd.conf"
tar -czf "${BUILD_DIR}/package.tgz" -C "${BUILD_DIR}/pkg" .

# ── SPK 顶层文件 ─────────────────────────────────────────────────────
sed 's/\r//' INFO > "${BUILD_DIR}/INFO"

for f in scripts/*; do
    sed 's/\r//' "$f" > "${BUILD_DIR}/scripts/$(basename $f)"
done
chmod +x "${BUILD_DIR}/scripts/"*

for f in conf/*; do
    sed 's/\r//' "$f" > "${BUILD_DIR}/conf/$(basename $f)"
done

cp PACKAGE_ICON.PNG     "${BUILD_DIR}/PACKAGE_ICON.PNG"
cp PACKAGE_ICON_256.PNG "${BUILD_DIR}/PACKAGE_ICON_256.PNG"

# ── 打包 .spk ────────────────────────────────────────────────────────
SPK="${DIST_DIR}/${PKG_NAME}-${VERSION}.spk"
tar -cf "${SPK}" -C "${BUILD_DIR}" \
    INFO package.tgz scripts conf \
    PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG

rm -rf "${BUILD_DIR}"

echo "==> 完成: ${SPK}"
echo "==> 套件中心 → 手动安装 上传此文件"
