// 等待页面加载完成后初始化粒子效果
window.addEventListener('load', function () {
  console.log('Window loaded, checking particles...')

  // 检查容器是否存在
  const container = document.getElementById('particles-js')
  if (!container) {
    console.error('Particles container not found!')
    return
  }

  // 检查 particlesJS 函数是否可用
  if (typeof particlesJS === 'undefined') {
    console.error('particlesJS function not found!')
    return
  }

  // 检测当前主题模式
  function isDarkMode() {
    return document.documentElement.classList.contains('tw-dark')
  }

  // 根据主题模式获取粒子颜色
  function getParticleColor() {
    return isDarkMode() ? "#8b5cf6" : "#646cff"  // 深色模式使用更亮的紫色
  }

  // 根据主题模式获取连线颜色
  function getLineColor() {
    return isDarkMode() ? "#8b5cf6" : "#646cff"
  }

  console.log('Initializing particles...')

  particlesJS("particles-js", {
    particles: {
      number: {
        value: 50,  // 增加粒子数量
        density: {
          enable: true,
          value_area: 800
        }
      },
      color: {
        value: getParticleColor()  // 使用主题色
      },
      shape: {
        type: "circle"
      },
      opacity: {
        value: 0.8,  // 增加透明度，从0.3改为0.8
        random: true,
        anim: {
          enable: true,
          speed: 1,
          opacity_min: 0.3,  // 增加最小透明度
          sync: false
        }
      },
      size: {
        value: 3,  // 增加粒子大小，从2改为3
        random: true,
        anim: {
          enable: true,
          speed: 2,
          size_min: 0.5,  // 增加最小尺寸
          sync: false
        }
      },
      line_linked: {
        enable: false,
        distance: 150,
        color: getLineColor(),  // 使用主题色
        opacity: 0.6,  // 增加连线透明度，从0.2改为0.6
        width: 1
      },
      move: {
        enable: true,
        speed: 2,  // 稍微增加速度
        direction: "none",
        random: true,
        straight: false,
        out_mode: "bounce",
        bounce: true
      }
    },
    interactivity: {
      detect_on: "canvas",
      events: {
        onhover: {
          enable: true,
          mode: "grab"
        },
        onclick: {
          enable: true,
          mode: "push"
        },
        resize: true
      },
      modes: {
        grab: {
          distance: 140,
          line_linked: {
            opacity: 0.8  // 增加悬停时的连线透明度
          }
        },
        push: {
          particles_nb: 4
        }
      }
    },
    retina_detect: true
  })

  console.log('Particles initialized successfully!')
})

// 暴露重新初始化函数供主题切换使用
window.reinitParticles = function () {
  console.log('Reinitializing particles...')

  // 销毁现有实例
  if (window.pJSDom && window.pJSDom[0]) {
    window.pJSDom[0].pJS.fn.vendors.destroypJS()
    window.pJSDom = []
  }

  // 检测当前主题模式
  function isDarkMode() {
    return document.documentElement.classList.contains('tw-dark')
  }

  // 根据主题模式获取粒子颜色
  function getParticleColor() {
    return isDarkMode() ? "#8b5cf6" : "#646cff"  // 深色模式使用更亮的紫色
  }

  // 根据主题模式获取连线颜色
  function getLineColor() {
    return isDarkMode() ? "#8b5cf6" : "#646cff"
  }

  // 重新初始化
  setTimeout(function () {
    if (typeof particlesJS !== 'undefined') {
      particlesJS("particles-js", {
        particles: {
          number: {
            value: 50,  // 增加粒子数量
            density: {
              enable: true,
              value_area: 800
            }
          },
          color: {
            value: getParticleColor()  // 使用主题色
          },
          shape: {
            type: "circle"
          },
          opacity: {
            value: 0.8,  // 增加透明度
            random: true,
            anim: {
              enable: true,
              speed: 1,
              opacity_min: 0.3,  // 增加最小透明度
              sync: false
            }
          },
          size: {
            value: 3,  // 增加粒子大小
            random: true,
            anim: {
              enable: true,
              speed: 2,
              size_min: 0.5,  // 增加最小尺寸
              sync: false
            }
          },
          line_linked: {
            enable: false,
            distance: 150,
            color: getLineColor(),  // 使用主题色
            opacity: 0.6,  // 增加连线透明度
            width: 1
          },
          move: {
            enable: true,
            speed: 2,  // 稍微增加速度
            direction: "none",
            random: true,
            straight: false,
            out_mode: "bounce",
            bounce: true
          }
        },
        interactivity: {
          detect_on: "canvas",
          events: {
            onhover: {
              enable: true,
              mode: "grab"
            },
            onclick: {
              enable: true,
              mode: "push"
            },
            resize: true
          },
          modes: {
            grab: {
              distance: 140,
              line_linked: {
                opacity: 0.8  // 增加悬停时的连线透明度
              }
            },
            push: {
              particles_nb: 4
            }
          }
        },
        retina_detect: true
      })
    }
  }, 100)
}