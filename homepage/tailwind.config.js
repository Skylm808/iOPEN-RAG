/** @type {import('tailwindcss').Config} */
module.exports = {
	prefix: 'tw-',
	important: false,
	content: [
		"./index.html",
		"./index.js",
		"./scripts/components.js",
	],
	darkMode: 'class',
	theme: {
		extend: {
			fontFamily: {
				poly: ['"poly"', "serif"],
			},
		},
	},
	plugins: [
		function ({ addVariant }) {
			addVariant('firefox', ':-moz-any(&)')
		}
	],
}

