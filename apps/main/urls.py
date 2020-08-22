from django.urls import path

from . import views
from .views import Overview

urlpatterns = [
    path('', Overview.as_view(), name = 'overview'),
]