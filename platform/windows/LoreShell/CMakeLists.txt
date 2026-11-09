cmake_minimum_required(VERSION 3.24)
project(LoreShell LANGUAGES CXX)

add_library(LoreShell SHARED
    dllmain.cpp
    ContextMenu.cpp
    OverlayHandler.cpp
    Registration.cpp
    Resources.rc
    LoreShell.def
)

target_compile_features(LoreShell PRIVATE cxx_std_20)
target_compile_definitions(LoreShell PRIVATE UNICODE _UNICODE WIN32_LEAN_AND_MEAN NOMINMAX)
target_include_directories(LoreShell PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(LoreShell PRIVATE shell32 ole32 uuid shlwapi gdi32)
if(MSVC)
    target_compile_options(LoreShell PRIVATE /W4 /permissive- /EHsc /Zi)
    target_link_options(LoreShell PRIVATE /DEBUG:FULL /MAP)
endif()

include(CTest)
if(BUILD_TESTING)
    add_executable(LoreShellMenuSmoke MenuSmoke.cpp)
    target_compile_features(LoreShellMenuSmoke PRIVATE cxx_std_20)
    target_compile_definitions(LoreShellMenuSmoke PRIVATE UNICODE _UNICODE WIN32_LEAN_AND_MEAN NOMINMAX)
    target_include_directories(LoreShellMenuSmoke PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
    target_link_libraries(LoreShellMenuSmoke PRIVATE LoreShell ole32)
    add_test(NAME LoreShellMenuSmoke COMMAND LoreShellMenuSmoke)
    set_tests_properties(LoreShellMenuSmoke PROPERTIES WORKING_DIRECTORY $<TARGET_FILE_DIR:LoreShell>)
endif()
