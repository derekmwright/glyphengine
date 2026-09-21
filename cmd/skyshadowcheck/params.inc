layout(set=1,binding=6,std140) uniform ApplicationParameters { vec4 values[256]; } app;
vec4 parameterColor() { return vec4(vec3(app.values[0].x + app.values[255].x),1); }
