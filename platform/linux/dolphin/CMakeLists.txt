# The Lore plugins for Dolphin: the context menu (a KFileItemAction plugin) and the status emblems
# (a KOverlayIcon plugin). Built against KDE Frameworks 5 by default, or 6 with -DLORE_KF=6.
cmake_minimum_required(VERSION 3.16)
project(lore-dolphin LANGUAGES CXX)

set(LORE_KF 5 CACHE STRING "KDE Frameworks major version: 5 or 6")
set(QT_MAJOR_VERSION ${LORE_KF})

find_package(ECM 5.90 REQUIRED NO_MODULE)
set(CMAKE_MODULE_PATH ${ECM_MODULE_PATH})
include(KDEInstallDirs)
include(KDECMakeSettings)
include(KDECompilerSettings NO_POLICY_SCOPE)

find_package(Qt${LORE_KF} REQUIRED COMPONENTS Core Widgets)
find_package(KF${LORE_KF} REQUIRED COMPONENTS CoreAddons KIO)

add_library(lore_dolphin_common STATIC lore_common.cpp)
set_target_properties(lore_dolphin_common PROPERTIES POSITION_INDEPENDENT_CODE ON)
target_link_libraries(lore_dolphin_common PUBLIC Qt${LORE_KF}::Core)

kcoreaddons_add_plugin(lore_fileitemaction SOURCES lore_fileitemaction.cpp INSTALL_NAMESPACE "kf${LORE_KF}/kfileitemaction")
target_link_libraries(lore_fileitemaction lore_dolphin_common KF${LORE_KF}::KIOWidgets Qt${LORE_KF}::Widgets)

kcoreaddons_add_plugin(lore_overlayicon SOURCES lore_overlayicon.cpp INSTALL_NAMESPACE "kf${LORE_KF}/overlayicon")
target_link_libraries(lore_overlayicon lore_dolphin_common KF${LORE_KF}::KIOWidgets)
