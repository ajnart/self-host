import QtQuick
import QtQuick.Effects
import Quickshell
import qs.Ui

BarWidget {
  id: root
  moduleName: "fyi.self-host"

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    tooltipText: "self-host · Click for Apps · Right-click for Dashboard"
    Accessible.name: "self-host"
    iconComponent: Component {
      Image {
        source: Qt.resolvedUrl("mark-mono-white.svg")
        fillMode: Image.PreserveAspectFit
        smooth: true
        layer.enabled: true
        layer.effect: MultiEffect {
          colorization: 1
          colorizationColor: button.foreground
        }
      }
    }

    onPressed: function(mouseButton) {
      if (mouseButton === Qt.LeftButton)
        Quickshell.execDetached(["/usr/bin/self-host-omarchy", "menu"])
      if (mouseButton === Qt.RightButton)
        Quickshell.execDetached(["/usr/bin/self-host-omarchy", "dashboard"])
    }
  }
}
